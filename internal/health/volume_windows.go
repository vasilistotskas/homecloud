//go:build windows

// Package health checks that the archive volume is the expected, healthy disk.
package health

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// bufLen is the UTF-16 buffer length for volume paths and volume GUID names.
const bufLen uint32 = windows.MAX_LONG_PATH

// VolumeGUID returns the volume GUID name (\\?\Volume{...}\) of the volume containing path.
func VolumeGUID(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("encode path %q: %w", path, err)
	}
	mount := make([]uint16, bufLen)
	if err := windows.GetVolumePathName(p, &mount[0], bufLen); err != nil {
		return "", fmt.Errorf("volume path of %q: %w", path, err)
	}
	name := make([]uint16, bufLen)
	if err := windows.GetVolumeNameForVolumeMountPoint(&mount[0], &name[0], bufLen); err != nil {
		return "", fmt.Errorf("volume name of %q: %w", windows.UTF16ToString(mount), err)
	}
	return windows.UTF16ToString(name), nil
}

// CheckVolume returns an error unless path lives on the volume named want.
func CheckVolume(path, want string) error {
	got, err := VolumeGUID(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("archive root %q is on volume %s, expected %s", path, got, want)
	}
	return nil
}

// Space reports free and total bytes available to the caller on the volume containing path.
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

// DiskSpace returns the space on the volume containing path.
func DiskSpace(path string) (Space, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Space{}, fmt.Errorf("encode path %q: %w", path, err)
	}
	var s Space
	if err := windows.GetDiskFreeSpaceEx(p, &s.Free, &s.Total, nil); err != nil {
		return Space{}, fmt.Errorf("disk space of %q: %w", path, err)
	}
	return s, nil
}

// CheckFreeSpace returns an error when free space is below minPercent.
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
