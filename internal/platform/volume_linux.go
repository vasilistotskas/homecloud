package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Filesystem UUIDs as exposed under /dev/disk/by-uuid: RFC 4122 form (ext4, xfs,
// btrfs), FAT/exFAT serials (XXXX-XXXX) and NTFS serials (16 hex digits).
var fsUUID = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{4}-[0-9a-fA-F]{4}|[0-9a-fA-F]{16})$`)

const (
	mountinfoPath = "/proc/self/mountinfo"
	byUUIDDir     = "/dev/disk/by-uuid"
)

// ValidateVolumeID accepts a filesystem UUID as listed in /dev/disk/by-uuid.
func ValidateVolumeID(id string) error {
	if !fsUUID.MatchString(id) {
		return fmt.Errorf("volume id must be a filesystem UUID as listed in %s, got %q", byUUIDDir, id)
	}
	return nil
}

// VolumeID returns the filesystem UUID of the volume containing path by matching
// its block device against /dev/disk/by-uuid. No privileges needed.
func VolumeID(path string) (string, error) {
	device, err := mountedDevice(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(byUUIDDir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", byUUIDDir, err)
	}
	for _, e := range entries {
		target, linkErr := filepath.EvalSymlinks(filepath.Join(byUUIDDir, e.Name()))
		if linkErr == nil && target == device {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("%w: device %s holding %q has no entry in %s", ErrNoVolumeIdentity, device, path, byUUIDDir)
}

// mountedDevice returns the block device (symlinks resolved) mounted where path
// lives. It finds the mount via statx(STATX_MNT_ID) (Linux ≥ 5.8) and
// /proc/self/mountinfo, and fails with ErrNoVolumeIdentity for mounts that are
// not backed by a block device (overlay, tmpfs, network file systems).
func mountedDevice(path string) (string, error) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID, &st); err != nil {
		return "", fmt.Errorf("statx %q: %w", path, err)
	}
	if st.Mask&unix.STATX_MNT_ID == 0 {
		return "", fmt.Errorf("statx %q: kernel did not report a mount id (Linux 5.8 or newer is required)", path)
	}

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", mountinfoPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only procfs handle
	source, err := mountSource(f, st.Mnt_id)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(source, "/dev/") {
		return "", fmt.Errorf("%w: %q is on %q, not a block device", ErrNoVolumeIdentity, path, source)
	}
	device, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("resolve mount source %q of %q: %w", source, path, err)
	}
	return device, nil
}

// mountSource returns the mount source of mount id in a mountinfo stream
// (proc_pid_mountinfo(5): "<id> <parent> <maj:min> <root> <mount point> <options>
// [optional fields] - <fstype> <source> <super options>").
func mountSource(r io.Reader, mountID uint64) (string, error) {
	want := strconv.FormatUint(mountID, 10)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != want {
			continue
		}
		for i, f := range fields {
			if f == "-" && i+2 < len(fields) {
				return fields[i+2], nil
			}
		}
		return "", fmt.Errorf("malformed mountinfo line for mount %s", want)
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read mountinfo: %w", err)
	}
	return "", errors.New("mount " + want + " not found in mountinfo")
}

// DiskSpace returns the space available to unprivileged callers on the volume containing path.
func DiskSpace(path string) (Space, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return Space{}, fmt.Errorf("statfs %q: %w", path, err)
	}
	frsize := uint64(s.Frsize) //nolint:gosec // fragment size is a small positive kernel value
	return Space{Free: s.Bavail * frsize, Total: s.Blocks * frsize}, nil
}
