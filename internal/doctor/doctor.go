// Package doctor runs read-only environment checks for homecloud.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/platform"
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
	return []Result{
		checkDir("archive root", cfg.Storage.Root),
		checkVolume(cfg.Storage),
		checkSpace(cfg.Storage),
		checkKeiConfig(cfg),
		checkPin(ctx, "kei version", kei.Pin(cfg.Kei).Check),
		checkPin(ctx, "ffprobe version", verify.FFprobePin(cfg.FFprobe).Check),
	}
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
	if err := kei.ConfigCurrent(cfg); err != nil {
		return Result{Name: "kei config", Err: err}
	}
	return Result{Name: "kei config", Detail: cfg.Kei.Config}
}
