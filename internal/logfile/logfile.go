// Package logfile writes a log to a file that is rotated by size.
//
// The current log is <dir>/<name><ext> (for example homecloud-agent.log).
// When a write would make it larger than the size limit, it is renamed to a
// backup named after the time of the rotation, <name>-<UTC time><ext> (for
// example homecloud-agent-20261006T224300Z.log, with -2, -3… for more
// rotations within one second), a fresh file is started, and the oldest
// backups beyond the limit are deleted. Backups are never renamed again, so
// a backup someone has open cannot stop a rotation, and names sort in time
// order. Files that do not match that pattern are never touched.
//
// Every file operation goes through an os.Root on the log's folder, so
// nothing outside it (for example through a symbolic link placed there) is
// ever written, renamed or deleted.
package logfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// Options configures a File.
type Options struct {
	// MaxBytes is the size at which the log is rotated (> 0). A single write
	// larger than this still goes whole into a fresh file.
	MaxBytes int64
	// MaxBackups is how many rotated files are kept (> 0).
	MaxBackups int
	// OnError is told when rotating or pruning fails. The log keeps being
	// written to the current file and rotation is retried at the next
	// write; OnError is called once per run of failures, outside the
	// File's lock, so it may write to this same File.
	OnError func(error)
}

// File is a size-rotated log file. It is safe for concurrent use.
type File struct {
	dir, name  string // the log's folder and file name in it
	opts       Options
	backupName *regexp.Regexp

	mu      sync.Mutex
	root    *os.Root
	f       *os.File
	size    int64
	closed  bool
	failing bool // the last rotation failed and was reported
}

// New returns a File writing to path. The folder and file are opened at the
// first write, and again after a failure, so a log on a drive that is not
// there yet (or was unplugged) is written once the drive is back.
func New(path string, opts Options) (*File, error) {
	if opts.MaxBytes <= 0 || opts.MaxBackups <= 0 || opts.OnError == nil {
		return nil, errors.New("logfile: MaxBytes, MaxBackups and OnError must be set")
	}
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	return &File{
		dir:  filepath.Dir(path),
		name: name,
		opts: opts,
		// <base>-<FileTimeLayout>[-<n>]<ext>; the layout is digits, T and Z.
		backupName: regexp.MustCompile(`^` + regexp.QuoteMeta(strings.TrimSuffix(name, ext)) +
			`-(\d{8}T\d{6}Z)(?:-(\d+))?` + regexp.QuoteMeta(ext) + `$`),
	}, nil
}

// Write appends p to the log, rotating first when p would make the current
// file larger than MaxBytes.
func (l *File) Write(p []byte) (int, error) {
	report, n, err := l.write(p)
	if report != nil {
		l.opts.OnError(report)
	}
	return n, err
}

func (l *File) write(p []byte) (report error, n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, 0, os.ErrClosed
	}
	if l.f == nil {
		if openErr := l.open(); openErr != nil {
			return nil, 0, openErr
		}
	}
	if l.size > 0 && l.size+int64(len(p)) > l.opts.MaxBytes {
		if rotErr := l.rotate(); rotErr != nil {
			if !l.failing {
				l.failing = true
				report = fmt.Errorf("rotate %s (still writing to it; retried at the next write): %w", l.path(), rotErr)
			}
			if l.f == nil {
				// The current file could not even be reopened.
				return report, 0, rotErr
			}
		} else {
			l.failing = false
			if pruneErr := l.prune(); pruneErr != nil {
				report = fmt.Errorf("delete old backups of %s: %w", l.path(), pruneErr)
			}
		}
	}
	n, err = l.f.Write(p)
	l.size += int64(n)
	if err != nil {
		// The drive may be gone: start over at the next write.
		l.drop()
		return report, n, fmt.Errorf("write %s: %w", l.path(), err)
	}
	return report, n, nil
}

func (l *File) path() string { return filepath.Join(l.dir, l.name) }

// open opens the log's folder (creating it) and the current file for
// appending; l.mu must be held. On failure everything is dropped, so the
// next attempt starts from the folder again.
func (l *File) open() error {
	if l.root == nil {
		if err := os.MkdirAll(l.dir, 0o700); err != nil {
			return fmt.Errorf("create log folder: %w", err)
		}
		root, err := os.OpenRoot(l.dir)
		if err != nil {
			return fmt.Errorf("open log folder: %w", err)
		}
		l.root = root
	}
	f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		l.drop()
		return fmt.Errorf("open %s: %w", l.path(), err)
	}
	info, err := f.Stat()
	if err != nil {
		l.f = f
		l.drop()
		return fmt.Errorf("stat %s: %w", l.path(), err)
	}
	l.f, l.size = f, info.Size()
	return nil
}

// drop closes the current file and the folder; l.mu must be held. Close
// errors are irrelevant here: the handles are being abandoned after a
// failure.
func (l *File) drop() {
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	if l.root != nil {
		_ = l.root.Close()
		l.root = nil
	}
}

// rotate renames the current file to a new backup and opens a fresh one.
// The file is closed first: on Windows an open file cannot be renamed. When
// the rename fails (another process holds the file), the current file is
// reopened and kept. l.mu must be held.
func (l *File) rotate() error {
	err := l.f.Close()
	l.f = nil
	if err != nil {
		err = fmt.Errorf("close %s: %w", l.path(), err)
	} else {
		var backup string
		if backup, err = l.freeBackupName(); err == nil {
			if err = l.root.Rename(l.name, backup); err != nil {
				err = fmt.Errorf("rename to %s: %w", backup, err)
			}
		}
	}
	if openErr := l.open(); openErr != nil {
		return errors.Join(err, openErr)
	}
	return err
}

// freeBackupName returns the name, in the log's folder, for a backup made
// now that no file has yet.
func (l *File) freeBackupName() (string, error) {
	ext := filepath.Ext(l.name)
	stem := strings.TrimSuffix(l.name, ext) + "-" + time.Now().UTC().Format(platform.FileTimeLayout)
	for n := 1; ; n++ {
		name := stem + ext
		if n > 1 {
			name = stem + "-" + strconv.Itoa(n) + ext
		}
		switch _, err := l.root.Lstat(name); {
		case errors.Is(err, fs.ErrNotExist):
			return name, nil
		case err != nil:
			return "", fmt.Errorf("check %s: %w", name, err)
		}
	}
}

// backup is a rotated file and its place in time order.
type backup struct {
	name string
	time string // FileTimeLayout sorts as text
	seq  int
}

// prune deletes the oldest backups beyond MaxBackups; l.mu must be held.
func (l *File) prune() error {
	entries, err := fs.ReadDir(l.root.FS(), ".")
	if err != nil {
		return fmt.Errorf("list log folder: %w", err)
	}
	var backups []backup
	for _, e := range entries {
		m := l.backupName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		seq := 1
		if m[2] != "" {
			if seq, err = strconv.Atoi(m[2]); err != nil {
				continue
			}
		}
		backups = append(backups, backup{name: e.Name(), time: m[1], seq: seq})
	}
	if len(backups) <= l.opts.MaxBackups {
		return nil
	}
	slices.SortFunc(backups, func(a, b backup) int {
		if c := strings.Compare(a.time, b.time); c != 0 {
			return c
		}
		return a.seq - b.seq
	})
	var errs []error
	for _, b := range backups[:len(backups)-l.opts.MaxBackups] {
		if err := l.root.Remove(b.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes the current file; later writes fail with os.ErrClosed.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var errs []error
	if l.f != nil {
		if err := l.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", l.path(), err))
		}
		l.f = nil
	}
	if l.root != nil {
		if err := l.root.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close log folder %s: %w", l.dir, err))
		}
		l.root = nil
	}
	return errors.Join(errs...)
}
