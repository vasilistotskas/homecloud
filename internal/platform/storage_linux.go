package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	sysfsRoot = "/sys"
	devRoot   = "/dev"
	// maxStackDepth bounds the walk from a file system down to its drives.
	// Real stacks (dm-crypt on LVM on md on a partition) are a few levels.
	maxStackDepth = 16
	// maxDevDepth bounds the search for device nodes: /dev/disk/by-id/<link>.
	maxDevDepth = 3
)

// StorageOf finds the drives holding path's file system and the md arrays
// between them. It walks sysfs from the file system's block device down
// through every layer (device-mapper, md, partitions) to the drives, so it
// needs no rights, and works in a container, whose sysfs shows the host's
// devices. A drive's Device is the node under /dev with its device number,
// "" when there is none (a drive not passed to the container).
func StorageOf(path string) (Storage, error) {
	m, err := mountOf(path)
	if err != nil {
		return Storage{}, err
	}
	return storageOf(sysfsRoot, devRoot, m)
}

func storageOf(sys, dev string, m mount) (Storage, error) {
	starts, err := fileSystemDevices(sys, m)
	if err != nil {
		return Storage{}, err
	}
	w := stackWalker{sys: sys, seen: map[string]bool{}, drives: map[string]string{}}
	for _, dir := range starts {
		if visitErr := w.visit(dir, 0); visitErr != nil {
			return Storage{}, visitErr
		}
	}
	nodes, err := blockNodes(dev)
	if err != nil {
		return Storage{}, err
	}
	var s Storage
	for name, dir := range w.drives {
		rdev, err := deviceNumber(dir)
		if err != nil {
			return Storage{}, err
		}
		s.Drives = append(s.Drives, Drive{Name: name, Device: bestNode(nodes[rdev], name)})
	}
	slices.SortFunc(s.Drives, func(a, b Drive) int { return strings.Compare(a.Name, b.Name) })
	s.Arrays = w.arrays
	slices.SortFunc(s.Arrays, func(a, b RaidArray) int { return strings.Compare(a.Name, b.Name) })
	return s, nil
}

// fileSystemDevices returns the sysfs directories of the block devices
// holding the file system mounted as m.
func fileSystemDevices(sys string, m mount) ([]string, error) {
	if unix.Major(m.dev) != 0 {
		num := fmt.Sprintf("%d:%d", unix.Major(m.dev), unix.Minor(m.dev))
		dir, err := filepath.EvalSymlinks(filepath.Join(sys, "dev", "block", num))
		if err != nil {
			return nil, fmt.Errorf("block device %s of %s: %w", num, m.source, err)
		}
		return []string{dir}, nil
	}
	// File systems such as btrfs report an anonymous device number; the
	// mount's source names one of their devices.
	dir, err := blockDirOf(sys, m.source)
	if err != nil {
		return nil, err
	}
	if m.fsType == "btrfs" {
		return btrfsDevices(sys, filepath.Base(dir))
	}
	return []string{dir}, nil
}

// blockDirOf finds the sysfs directory of the block device a mount source
// names: /dev/mapper/<name> by its device-mapper name, anything else by its
// kernel name. The source's own node need not exist here: in a container it
// is the host's path.
func blockDirOf(sys, source string) (string, error) {
	classDir := filepath.Join(sys, "class", "block")
	if dmName, ok := strings.CutPrefix(source, "/dev/mapper/"); ok {
		entries, err := os.ReadDir(classDir)
		if err != nil {
			return "", fmt.Errorf("list block devices: %w", err)
		}
		for _, e := range entries {
			name, err := readAttr(filepath.Join(classDir, e.Name(), "dm", "name"))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if name == dmName {
				dir, err := filepath.EvalSymlinks(filepath.Join(classDir, e.Name()))
				if err != nil {
					return "", fmt.Errorf("device-mapper device %s: %w", dmName, err)
				}
				return dir, nil
			}
		}
		return "", fmt.Errorf("%w: no device-mapper device is named %s", ErrHealthUnsupported, dmName)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(classDir, filepath.Base(source)))
	if err != nil {
		return "", fmt.Errorf("%w: no block device is named after %s: %w", ErrHealthUnsupported, source, err)
	}
	return dir, nil
}

// btrfsDevices returns every device of the btrfs file system that has the
// block device named member, from /sys/fs/btrfs/<fsid>/devices.
func btrfsDevices(sys, member string) ([]string, error) {
	fsDirs, err := filepath.Glob(filepath.Join(sys, "fs", "btrfs", "*", "devices"))
	if err != nil {
		return nil, fmt.Errorf("list btrfs file systems: %w", err)
	}
	for _, devices := range fsDirs {
		entries, err := os.ReadDir(devices)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", devices, err)
		}
		if !slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == member }) {
			continue
		}
		dirs := make([]string, 0, len(entries))
		for _, e := range entries {
			dir, err := filepath.EvalSymlinks(filepath.Join(devices, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("btrfs device %s: %w", e.Name(), err)
			}
			dirs = append(dirs, dir)
		}
		return dirs, nil
	}
	return nil, fmt.Errorf("no btrfs file system in %s has the device %s", filepath.Join(sys, "fs", "btrfs"), member)
}

// stackWalker follows a block device's slaves down to the drives.
type stackWalker struct {
	sys    string
	seen   map[string]bool
	drives map[string]string // name → sysfs directory
	arrays []RaidArray
}

func (w *stackWalker) visit(dir string, depth int) error {
	if depth > maxStackDepth {
		return fmt.Errorf("block device stack under %s is deeper than %d levels", dir, maxStackDepth)
	}
	if w.seen[dir] {
		return nil
	}
	w.seen[dir] = true

	isArray := false
	if info, err := os.Stat(filepath.Join(dir, "md")); err == nil && info.IsDir() {
		isArray = true
		a, err := readArray(dir)
		if err != nil {
			return err
		}
		w.arrays = append(w.arrays, a)
	}
	slaves, err := os.ReadDir(filepath.Join(dir, "slaves"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("list the devices under %s: %w", filepath.Base(dir), err)
	}
	for _, s := range slaves {
		next, err := filepath.EvalSymlinks(filepath.Join(dir, "slaves", s.Name()))
		if err != nil {
			return fmt.Errorf("device %s under %s: %w", s.Name(), filepath.Base(dir), err)
		}
		if err := w.visit(next, depth+1); err != nil {
			return err
		}
	}
	if len(slaves) > 0 || isArray {
		// A layer, not a drive; an array without members is reported as such.
		return nil
	}
	disk := diskOf(dir)
	w.drives[filepath.Base(disk)] = disk
	return nil
}

// diskOf returns the whole disk of a partition's sysfs directory (its parent:
// /sys/devices/…/block/sda/sda3), or dir itself.
func diskOf(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		return filepath.Dir(dir)
	}
	return dir
}

// readArray reads an md array's state from <dir>/md. Attributes that only
// arrays with redundancy have are left zero when absent.
func readArray(dir string) (RaidArray, error) {
	md := filepath.Join(dir, "md")
	a := RaidArray{Name: filepath.Base(dir)}
	var err error
	if a.Level, err = readAttr(filepath.Join(md, "level")); err != nil {
		return RaidArray{}, err
	}
	if a.ArrayState, err = readAttr(filepath.Join(md, "array_state")); err != nil {
		return RaidArray{}, err
	}
	if a.RaidDisks, err = readInt(filepath.Join(md, "raid_disks")); err != nil {
		return RaidArray{}, err
	}
	if a.Degraded, err = optional(readInt(filepath.Join(md, "degraded"))); err != nil {
		return RaidArray{}, err
	}
	if a.SyncAction, err = optional(readAttr(filepath.Join(md, "sync_action"))); err != nil {
		return RaidArray{}, err
	}
	completed, err := optional(readAttr(filepath.Join(md, "sync_completed")))
	if err != nil {
		return RaidArray{}, err
	}
	if a.SyncDone, a.SyncTotal, err = parseSyncCompleted(completed); err != nil {
		return RaidArray{}, fmt.Errorf("%s: %w", a.Name, err)
	}
	if a.MismatchCount, err = optional(readUint(filepath.Join(md, "mismatch_cnt"))); err != nil {
		return RaidArray{}, err
	}

	members, err := filepath.Glob(filepath.Join(md, "dev-*"))
	if err != nil {
		return RaidArray{}, fmt.Errorf("list members of %s: %w", a.Name, err)
	}
	for _, m := range members {
		member, err := filepath.EvalSymlinks(filepath.Join(m, "block"))
		if err != nil {
			return RaidArray{}, fmt.Errorf("member %s of %s: %w", filepath.Base(m), a.Name, err)
		}
		state, err := readAttr(filepath.Join(m, "state"))
		if err != nil {
			return RaidArray{}, err
		}
		errs, err := readUint(filepath.Join(m, "errors"))
		if err != nil {
			return RaidArray{}, err
		}
		a.Members = append(a.Members, RaidMember{
			Name:   filepath.Base(member),
			Drive:  filepath.Base(diskOf(member)),
			States: strings.Split(state, ","),
			Errors: errs,
		})
	}
	slices.SortFunc(a.Members, func(x, y RaidMember) int { return strings.Compare(x.Name, y.Name) })
	return a, nil
}

// parseSyncCompleted reads md's sync_completed: "none" when no sync runs,
// "delayed" while one waits, otherwise "<done> / <total>" sectors.
func parseSyncCompleted(s string) (done, total uint64, err error) {
	switch s {
	case "", "none", "delayed":
		return 0, 0, nil
	}
	d, t, ok := strings.Cut(s, " / ")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected sync_completed %q", s)
	}
	if done, err = strconv.ParseUint(d, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("sync_completed %q: %w", s, err)
	}
	if total, err = strconv.ParseUint(t, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("sync_completed %q: %w", s, err)
	}
	return done, total, nil
}

// deviceNumber reads a block device's number from <dir>/dev ("8:0").
func deviceNumber(dir string) (uint64, error) {
	s, err := readAttr(filepath.Join(dir, "dev"))
	if err != nil {
		return 0, err
	}
	maj, minor, ok := strings.Cut(s, ":")
	ma, majErr := strconv.ParseUint(maj, 10, 32)
	mi, minErr := strconv.ParseUint(minor, 10, 32)
	if !ok || majErr != nil || minErr != nil {
		return 0, fmt.Errorf("malformed device number %q for %s", s, filepath.Base(dir))
	}
	return unix.Mkdev(uint32(ma), uint32(mi)), nil
}

// blockNodes indexes the block device nodes under dev (and symbolic links
// to them, such as /dev/disk/by-id/…) by device number.
func blockNodes(dev string) (map[uint64][]string, error) {
	nodes := map[uint64][]string{}
	err := filepath.WalkDir(dev, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dev {
				return err
			}
			// A directory that cannot be read holds no node this process could
			// use either.
			return fs.SkipDir
		}
		depth := strings.Count(strings.TrimPrefix(p, dev), string(filepath.Separator))
		if d.IsDir() {
			if depth >= maxDevDepth {
				return fs.SkipDir
			}
			return nil
		}
		if rdev, ok := blockDevice(p); ok {
			nodes[rdev] = append(nodes[rdev], p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list device nodes in %s: %w", dev, err)
	}
	return nodes, nil
}

// blockDevice reports the device number of the block device at p (following
// a symbolic link); ok is false for anything else, including a dangling link.
func blockDevice(p string) (rdev uint64, ok bool) {
	var st unix.Stat_t
	if unix.Stat(p, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFBLK {
		return 0, false
	}
	return st.Rdev, true
}

// bestNode picks the node to open a drive by: the one named after the drive
// (/dev/sda), else the first by path, or "" when there is none.
func bestNode(paths []string, name string) string {
	if i := slices.IndexFunc(paths, func(p string) bool { return filepath.Base(p) == name }); i >= 0 {
		return paths[i]
	}
	if len(paths) == 0 {
		return ""
	}
	return slices.Min(paths)
}

func readAttr(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // sysfs attributes found by the walk, never user input
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func readInt(path string) (int, error) {
	s, err := readAttr(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

func readUint(path string) (uint64, error) {
	s, err := readAttr(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

// optional turns a missing attribute into its zero value.
func optional[T any](v T, err error) (T, error) {
	if errors.Is(err, fs.ErrNotExist) {
		var zero T
		return zero, nil
	}
	return v, err
}
