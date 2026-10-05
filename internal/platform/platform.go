// Package platform isolates every operating-system-specific behaviour of
// homecloud behind one small API, implemented in *_windows.go and *_linux.go.
// Only Windows and Linux are supported; other targets fail to compile.
//
// Per-OS functions:
//
//	VolumeID(path) (string, error)       identity of the volume holding path
//	ValidateVolumeID(id) error           syntax of a configured volume identity
//	DiskSpace(path) (Space, error)       free/total bytes available to the caller
//	AcquireInstance(name) (*Lock, error) single-instance guard for the agent
//	InstallAgent(ctx, AgentService)      start the agent at logon, and now
//	UninstallAgent(ctx, name)            remove that registration
//	NotifyReady() / NotifyStopping()     service-manager readiness protocol
package platform

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrAlreadyRunning is returned by AcquireInstance when another process holds the lock.
	ErrAlreadyRunning = errors.New("another instance is already running")
	// ErrNoVolumeIdentity is returned by VolumeID for storage without a stable
	// volume identity (for example overlay, tmpfs or network file systems).
	ErrNoVolumeIdentity = errors.New("path is not on a volume with a stable identity")
)

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

// AgentService describes how the background agent is started at logon.
type AgentService struct {
	Name        string // task / unit name, e.g. "homecloud-agent"
	Description string
	Executable  string // absolute path to homecloud-agent
	WorkingDir  string
}

// CheckVolume returns an error unless path lives on the volume identified by want.
func CheckVolume(path, want string) error {
	got, err := VolumeID(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("archive root %q is on volume %s, expected %s", path, got, want)
	}
	return nil
}

// CheckFreeSpace returns the volume's space and an error when free space is below minPercent.
func CheckFreeSpace(path string, minPercent float64) (Space, error) {
	s, err := DiskSpace(path)
	if err != nil {
		return Space{}, err
	}
	if s.FreePercent() < minPercent {
		return s, fmt.Errorf("volume of %q has %.1f%% free, minimum is %.1f%%", path, s.FreePercent(), minPercent)
	}
	return s, nil
}
