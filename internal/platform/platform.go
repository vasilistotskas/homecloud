// Package platform isolates every operating-system-specific behaviour of
// homecloud behind one small API, implemented in *_windows.go and *_linux.go.
// Only Windows and Linux are supported; other targets fail to compile.
//
// Per-OS functions:
//
//	SameVolume(a, b) (bool, error)       whether two paths share a volume
//	DiskSpace(path) (Space, error)       free/total bytes available to the caller
//	AcquireInstance(name) (*Lock, error) single-instance guard for the agent
//	InstallTask(ctx, Task)               start the agent at logon (and now), or run
//	                                     a job every Task.Every after logon
//	UninstallTask(ctx, name)             remove that registration
//	NotifyReady() / NotifyStopping()     service-manager readiness protocol
//	StateDir() (string, error)           per-user runtime state directory
//	WriteSecretFile(path, data) error    atomic write readable only by the current user
//	CheckPrivate(dir) error              whether a folder is the current user's alone
//	MakePrivate(dir) (bool, error)       make it so, for everything inside it too
//	IsDiskFull(err) bool                 whether an error means the volume is full
//
// Shared by both: WriteFile (atomic write of a file others may read) and
// WriteFileIfChanged.
//
//	OpenURL(url) error                   open a URL in the default browser
//	MarkHidden(path) error               hide a file or folder from file managers
//	FileID(*os.File) (FileIdentity, error) what identifies a file (shared by its hard links)
//	StorageOf(path) (Storage, error)     drives (and Linux md arrays) holding path
//	ReadDriveHealth(ctx, Drive)          SMART health the OS itself reports for a
//	                                     drive (Windows: NVMe health log; Linux: udisks)
package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// ErrAlreadyRunning is returned by AcquireInstance when another process holds the lock.
var ErrAlreadyRunning = errors.New("another instance is already running")

// Space is the free and total space on a volume, as available to the caller.
type Space struct {
	Free, Total uint64
}

// FreePercent is free space as a percentage of total.
func (s Space) FreePercent() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Free) / float64(s.Total) * 100
}

// Task is a program the user's service manager runs: the agent, started at
// logon and restarted when it fails (Every 0), or a job run every Every while
// the user is logged on, such as "homecloud iphone push".
type Task struct {
	Name        string // task / unit name, e.g. "homecloud-agent"
	Description string
	Executable  string   // absolute path
	Args        []string // arguments passed to it
	WorkingDir  string
	// Every is a job's interval, whole minutes from 1m to 31 days (Task
	// Scheduler's range for a repetition); 0 for the agent.
	Every time.Duration
}

// Repetition bounds of Task.Every (Task Scheduler schema, repetitionType).
const (
	minTaskEvery = time.Minute
	maxTaskEvery = 31 * 24 * time.Hour
)

// validate checks what both service managers need of a task.
func (t Task) validate() error {
	if !filepath.IsAbs(t.Executable) || !filepath.IsAbs(t.WorkingDir) {
		return fmt.Errorf("task executable and working dir must be absolute: %q, %q", t.Executable, t.WorkingDir)
	}
	if t.Every != 0 && (t.Every < minTaskEvery || t.Every > maxTaskEvery || t.Every%time.Minute != 0) {
		return fmt.Errorf("a task's interval must be whole minutes from %s to %s, got %s", minTaskEvery, maxTaskEvery, t.Every)
	}
	return nil
}

// ErrLowSpace marks a volume whose free space is below the configured minimum.
var ErrLowSpace = errors.New("free space below minimum")

// CheckFreeSpace returns the volume's space, and an ErrLowSpace-wrapping error
// when free space is below minPercent.
func CheckFreeSpace(path string, minPercent float64) (Space, error) {
	s, err := DiskSpace(path)
	if err != nil {
		return Space{}, err
	}
	if s.FreePercent() < minPercent {
		return s, fmt.Errorf("%w: volume of %q has %.1f%% free, minimum is %.1f%%", ErrLowSpace, path, s.FreePercent(), minPercent)
	}
	return s, nil
}
