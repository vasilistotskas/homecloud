// Package doctor runs read-only environment checks for homecloud.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
	"github.com/vasilistotskas/homecloud/internal/verify"
)

// Result is the outcome of one check.
type Result struct {
	Name   string
	Detail string
	Err    error
}

// OK reports whether the check passed.
func (r Result) OK() bool { return r.Err == nil }

// Run executes every check; it never stops early so all problems are visible.
func Run(ctx context.Context, cfg *config.Config) []Result {
	results := []Result{
		checkDir("archive root", cfg.Storage.Root),
		checkVolume(cfg.Storage),
		checkSpace(cfg.Storage),
		checkBackupVolume(cfg),
		checkKeiConfig(cfg),
		checkPin(ctx, "kei version", kei.Pin(cfg.Kei).Check),
		checkPin(ctx, "ffprobe version", verify.FFprobePin(cfg.FFmpeg).Check),
		checkPin(ctx, "ffmpeg version", thumbs.FFmpegPin(cfg.FFmpeg).Check),
	}
	return append(results, checkIPhoneBackupDirs(cfg)...)
}

// Err joins the failures in results, or returns nil when all passed.
func Err(results []Result) error {
	var errs []error
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Name, r.Err))
		}
	}
	return errors.Join(errs...)
}

func checkDir(name, path string) Result {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return Result{Name: name, Err: err}
	case !info.IsDir():
		return Result{Name: name, Err: fmt.Errorf("%s is not a directory", path)}
	default:
		return Result{Name: name, Detail: path}
	}
}

func checkVolume(s config.Storage) Result {
	if err := platform.CheckVolume(s.Root, s.VolumeID); err != nil {
		return Result{Name: "archive volume", Err: err}
	}
	return Result{Name: "archive volume", Detail: s.VolumeID}
}

func checkSpace(s config.Storage) Result {
	space, err := platform.CheckFreeSpace(s.Root, s.MinFreePercent)
	detail := fmt.Sprintf("%.1f%% free (%.0f of %.0f GB)", space.FreePercent(), float64(space.Free)/1e9, float64(space.Total)/1e9)
	return Result{Name: "free space", Detail: detail, Err: err}
}

func checkPin(ctx context.Context, name string, check func(context.Context) error) Result {
	if err := check(ctx); err != nil {
		return Result{Name: name, Err: err}
	}
	return Result{Name: name, Detail: "pinned version present"}
}

// checkKeiConfig reports whether kei's configuration matches homecloud's
// settings. Only the agent writes it (at startup), so doctor stays read-only.
func checkKeiConfig(cfg *config.Config) Result {
	err := kei.ConfigCurrent(cfg)
	if errors.Is(err, fs.ErrNotExist) {
		// A fresh install: nothing is wrong, the agent writes it on its first start.
		return Result{Name: "kei config", Detail: cfg.Kei.Config + " (not written yet; the agent writes it when it starts)"}
	}
	if err != nil {
		return Result{Name: "kei config", Err: err}
	}
	return Result{Name: "kei config", Detail: cfg.Kei.Config}
}

// checkBackupVolume requires ledger snapshots to live off the archive volume;
// a copy on the archive drive would be lost together with it. The directory
// itself is created by the first backup, so its nearest existing ancestor
// decides the volume.
func checkBackupVolume(cfg *config.Config) Result {
	const name = "ledger backups"
	dir := cfg.Backup.Dir
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Result{Name: name, Err: err}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Result{Name: name, Err: fmt.Errorf("no part of %s exists", cfg.Backup.Dir)}
		}
		dir = parent
	}
	got, err := platform.VolumeID(dir)
	if err != nil {
		return Result{Name: name, Err: err}
	}
	if strings.EqualFold(got, cfg.Storage.VolumeID) {
		return Result{Name: name, Err: fmt.Errorf("backup.dir %s is on the archive volume; it must be on another drive", cfg.Backup.Dir)}
	}
	return Result{Name: name, Detail: cfg.Backup.Dir}
}

// checkIPhoneBackupDirs reports each configured iPhone backup directory. A
// directory that does not exist yet is normal before the first backup: Apple's
// tool creates it then.
func checkIPhoneBackupDirs(cfg *config.Config) []Result {
	out := make([]Result, 0, len(cfg.IPhoneBackup.Dirs))
	for _, dir := range cfg.IPhoneBackup.Dirs {
		info, err := os.Stat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out = append(out, Result{Name: "iphone backups", Detail: dir + " (no backup yet; created by the first backup)"})
		case err != nil:
			out = append(out, Result{Name: "iphone backups", Err: err})
		case !info.IsDir():
			out = append(out, Result{Name: "iphone backups", Err: fmt.Errorf("%s is not a directory", dir)})
		default:
			out = append(out, Result{Name: "iphone backups", Detail: dir})
		}
	}
	return out
}
