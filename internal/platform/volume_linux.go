package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	mountinfoPath = "/proc/self/mountinfo"
	byUUIDDir     = "/dev/disk/by-uuid"
)

// SameVolume reports whether a and b, existing paths, are on the same
// filesystem. It compares device numbers, which also works for bind mounts in
// a container, where no volume UUID is visible.
func SameVolume(a, b string) (bool, error) {
	var sa, sb unix.Stat_t
	if err := unix.Stat(a, &sa); err != nil {
		return false, fmt.Errorf("stat %q: %w", a, err)
	}
	if err := unix.Stat(b, &sb); err != nil {
		return false, fmt.Errorf("stat %q: %w", b, err)
	}
	return sa.Dev == sb.Dev, nil
}

// VolumeID returns the filesystem UUID of the volume containing path by matching
// the mount against /dev/disk/by-uuid. No privileges needed.
func VolumeID(path string) (string, error) {
	m, err := mountOf(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(byUUIDDir)
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %w", ErrNoVolumeIdentity, byUUIDDir, err)
	}
	for _, e := range entries {
		target, linkErr := filepath.EvalSymlinks(filepath.Join(byUUIDDir, e.Name()))
		if linkErr != nil {
			continue
		}
		var st unix.Stat_t
		if statErr := unix.Stat(target, &st); statErr != nil {
			continue
		}
		if m.matches(target, st.Rdev) {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("%w: the mount holding %q (device %d:%d, source %s) has no entry in %s",
		ErrNoVolumeIdentity, path, unix.Major(m.dev), unix.Minor(m.dev), m.source, byUUIDDir)
}

// mount is the filesystem mount holding a path, as /proc/self/mountinfo reports it.
type mount struct {
	// dev is the st_dev of files on the mount (mountinfo's major:minor).
	dev uint64
	// source is the mount source, e.g. /dev/sda1, /dev/root or overlay.
	source string
}

// matches reports whether the block device at devicePath, with device number
// rdev, holds m. Most block-backed filesystems report the device's own number,
// which also covers sources such as /dev/root that are not device nodes.
// Filesystems that report an anonymous number (major 0, e.g. btrfs) are
// matched by their source device instead.
func (m mount) matches(devicePath string, rdev uint64) bool {
	if unix.Major(m.dev) != 0 {
		return rdev == m.dev
	}
	source, err := filepath.EvalSymlinks(m.source)
	return err == nil && source == devicePath
}

// mountOf finds the mount holding path via statx(STATX_MNT_ID) (Linux ≥ 5.8)
// and /proc/self/mountinfo. It fails with ErrNoVolumeIdentity for mounts that
// are not backed by a block device (overlay, tmpfs, network file systems).
func mountOf(path string) (mount, error) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID, &st); err != nil {
		return mount{}, fmt.Errorf("statx %q: %w", path, err)
	}
	if st.Mask&unix.STATX_MNT_ID == 0 {
		return mount{}, fmt.Errorf("statx %q: kernel did not report a mount id (Linux 5.8 or newer is required)", path)
	}

	f, err := os.Open(mountinfoPath)
	if err != nil {
		return mount{}, fmt.Errorf("open %s: %w", mountinfoPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only procfs handle
	m, err := parseMount(f, st.Mnt_id)
	if err != nil {
		return mount{}, err
	}
	if !strings.HasPrefix(m.source, "/dev/") {
		return mount{}, fmt.Errorf("%w: %q is on %q, not a block device", ErrNoVolumeIdentity, path, m.source)
	}
	return m, nil
}

// parseMount returns mount id's device number and source from a mountinfo
// stream (proc_pid_mountinfo(5): "<id> <parent> <maj:min> <root> <mount point>
// <options> [optional fields] - <fstype> <source> <super options>").
func parseMount(r io.Reader, mountID uint64) (mount, error) {
	want := strconv.FormatUint(mountID, 10)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != want {
			continue
		}
		if len(fields) < 3 {
			return mount{}, fmt.Errorf("malformed mountinfo line for mount %s", want)
		}
		majStr, minStr, ok := strings.Cut(fields[2], ":")
		maj, majErr := strconv.ParseUint(majStr, 10, 32)
		minor, minErr := strconv.ParseUint(minStr, 10, 32)
		if !ok || majErr != nil || minErr != nil {
			return mount{}, fmt.Errorf("malformed device %q in mountinfo line for mount %s", fields[2], want)
		}
		for i, field := range fields {
			if field == "-" && i+2 < len(fields) {
				return mount{dev: unix.Mkdev(uint32(maj), uint32(minor)), source: fields[i+2]}, nil
			}
		}
		return mount{}, fmt.Errorf("malformed mountinfo line for mount %s", want)
	}
	if err := sc.Err(); err != nil {
		return mount{}, fmt.Errorf("read mountinfo: %w", err)
	}
	return mount{}, errors.New("mount " + want + " not found in mountinfo")
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
