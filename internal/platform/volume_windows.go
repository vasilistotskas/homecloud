package platform

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// bufLen is the UTF-16 buffer length for volume paths and volume GUID names.
const bufLen uint32 = windows.MAX_LONG_PATH

// VolumeID returns the volume GUID name of the volume containing path.
func VolumeID(path string) (string, error) {
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

// SameVolume reports whether a and b, existing paths, are on the same volume.
func SameVolume(a, b string) (bool, error) {
	// GetVolumePathName also resolves paths that do not exist.
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); err != nil {
			return false, fmt.Errorf("stat %q: %w", p, err)
		}
	}
	va, err := VolumeID(a)
	if err != nil {
		return false, err
	}
	vb, err := VolumeID(b)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(va, vb), nil
}

// DiskSpace returns the space available to the caller on the volume containing path.
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
