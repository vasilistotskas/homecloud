package platform

import (
	"fmt"
	"regexp"

	"golang.org/x/sys/windows"
)

// bufLen is the UTF-16 buffer length for volume paths and volume GUID names.
const bufLen uint32 = windows.MAX_LONG_PATH

var volumeGUIDName = regexp.MustCompile(`^\\\\\?\\Volume\{[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\}\\$`)

// ValidateVolumeID accepts a volume GUID name: \\?\Volume{GUID}\ .
func ValidateVolumeID(id string) error {
	if !volumeGUIDName.MatchString(id) {
		return fmt.Errorf(`volume id must be a volume GUID name like \\?\Volume{GUID}\, got %q`, id)
	}
	return nil
}

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
