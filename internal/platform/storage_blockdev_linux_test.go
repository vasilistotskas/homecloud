package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestStorageOfRealStack builds UGOS Pro's storage layout from loop devices,
// md raid1 → LVM → ext4 on partitions of two "drives" (plus a btrfs raid1
// when the kernel has btrfs), and checks StorageOf against what the kernel
// reports, before and after a drive fails. It needs root, the md raid1
// personality, and mdadm, lvm2, e2fsprogs, fdisk (sfdisk) and btrfs-progs;
// it creates kernel devices, so it runs only when asked:
//
//	HOMECLOUD_TEST_BLOCK_DEVICES=1 go test -run TestStorageOfRealStack ./internal/platform/
func TestStorageOfRealStack(t *testing.T) {
	if os.Getenv("HOMECLOUD_TEST_BLOCK_DEVICES") != "1" {
		t.Skip("set HOMECLOUD_TEST_BLOCK_DEVICES=1 to build real block devices (needs root)")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HOMECLOUD_TEST_BLOCK_DEVICES needs root")
	}
	work := t.TempDir()
	vg := fmt.Sprintf("hctest%d", os.Getpid())

	// Two "drives", each with a raid partition and a btrfs partition.
	var drives, mdParts, btrfsParts []string
	for i := range 2 {
		img := filepath.Join(work, fmt.Sprintf("drive%d.img", i))
		run(t, "", "truncate", "-s", "320M", img)
		run(t, "label: gpt\nsize=150M, type=R\nsize=150M, type=L\n", "sfdisk", "--quiet", img)
		loop := strings.TrimSpace(run(t, "", "losetup", "--find", "--show", "--partscan", img))
		t.Cleanup(func() { run(t, "", "losetup", "--detach", loop) })
		drives = append(drives, filepath.Base(loop))
		mdParts = append(mdParts, partitionNode(t, loop, 1))
		btrfsParts = append(btrfsParts, partitionNode(t, loop, 2))
	}

	md := "/dev/md/" + vg
	run(t, "", "mdadm", "--create", md, "--run", "--assume-clean", "--metadata=1.2", "--level=1",
		"--raid-devices=2", mdParts[0], mdParts[1])
	t.Cleanup(func() { run(t, "", "mdadm", "--stop", md) })
	mdName := filepath.Base(evalSymlinks(t, md))

	// Without udev (a container) LVM must create its own nodes.
	lvm := []string{"--config", "activation { udev_sync=0 udev_rules=0 } devices { obtain_device_list_from_udev=0 }"}
	run(t, "", "pvcreate", append(lvm, "--quiet", md)...)
	run(t, "", "vgcreate", append(lvm, "--quiet", vg, md)...)
	t.Cleanup(func() { run(t, "", "vgchange", append(lvm, "--quiet", "--activate", "n", vg)...) })
	run(t, "", "lvcreate", append(lvm, "--quiet", "--yes", "--name", "volume1", "--extents", "100%FREE", vg)...)
	run(t, "", "vgmknodes", append(lvm, vg)...)
	volume := "/dev/mapper/" + vg + "-volume1"
	run(t, "", "mkfs.ext4", "-q", volume)
	mnt := mountFS(t, volume, filepath.Join(work, "volume1"))

	got, err := StorageOf(mnt)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("storage: %+v", got)
	wantDrives(t, got, drives)
	if len(got.Arrays) != 1 {
		t.Fatalf("arrays = %+v, want %s", got.Arrays, mdName)
	}
	a := got.Arrays[0]
	if a.Name != mdName || a.Level != "raid1" || a.RaidDisks != 2 || a.Degraded != 0 || a.SyncAction != "idle" || len(a.Members) != 2 {
		t.Fatalf("array = %+v", a)
	}
	for i, m := range a.Members {
		if m.Name != filepath.Base(mdParts[i]) || m.Drive != drives[i] || !slices.Equal(m.States, []string{"in_sync"}) {
			t.Fatalf("member %d = %+v", i, m)
		}
	}

	// The second drive fails: the array is degraded and says which member.
	run(t, "", "mdadm", md, "--fail", mdParts[1])
	got, err = StorageOf(mnt)
	if err != nil {
		t.Fatal(err)
	}
	a = got.Arrays[0]
	if a.Degraded != 1 || !slices.Contains(a.Members[1].States, "faulty") {
		t.Fatalf("after a failure: array = %+v", a)
	}

	t.Run("btrfs raid1", func(t *testing.T) {
		filesystems, err := os.ReadFile("/proc/filesystems")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(filesystems, []byte("\tbtrfs\n")) {
			t.Skip("this kernel has no btrfs")
		}
		run(t, "", "mkfs.btrfs", "--quiet", "--force", "--data", "raid1", "--metadata", "raid1", btrfsParts[0], btrfsParts[1])
		// device= registers the other device at mount time; a container has
		// no /dev/btrfs-control for "btrfs device scan".
		got, err := StorageOf(mountFS(t, btrfsParts[0], filepath.Join(work, "btrfs"),
			"-o", "device="+btrfsParts[0]+",device="+btrfsParts[1]))
		if err != nil {
			t.Fatal(err)
		}
		wantDrives(t, got, drives)
		if len(got.Arrays) != 0 {
			t.Fatalf("arrays = %+v, want none", got.Arrays)
		}
	})
}

func wantDrives(t *testing.T, got Storage, names []string) {
	t.Helper()
	want := make([]Drive, 0, len(names))
	for _, n := range names {
		want = append(want, Drive{Name: n, Device: "/dev/" + n})
	}
	if !slices.Equal(got.Drives, want) {
		t.Fatalf("drives = %+v, want %+v", got.Drives, want)
	}
}

// partitionNode returns partition n's device node of loop, creating it from
// sysfs where nothing (udev, devtmpfs) does, as in a container.
// partitionWait bounds how long a loop device's partitions take to appear:
// when udev opens the new device at once, the kernel's partition scan for
// losetup --partscan completes only after it.
const partitionWait = 10 * time.Second

func partitionNode(t *testing.T, loop string, n int) string {
	t.Helper()
	name := fmt.Sprintf("%sp%d", filepath.Base(loop), n)
	node := "/dev/" + name
	if _, err := os.Stat(node); err == nil {
		return node
	}
	sys := filepath.Join(sysfsRoot, "class", "block", name)
	for deadline := time.Now().Add(partitionWait); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(sys); err == nil || time.Now().After(deadline) {
			break
		}
	}
	rdev, err := deviceNumber(sys)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(node, unix.S_IFBLK|0o600, int(rdev)); err != nil { //nolint:gosec // a kernel device number fits
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(node); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	})
	return node
}

func mountFS(t *testing.T, device, dir string, options ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	run(t, "", "mount", append(options, device, dir)...)
	t.Cleanup(func() { run(t, "", "umount", dir) })
	return dir
}

func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// run runs a tool to completion. Cleanups use it too, after t.Context() is
// cancelled, so it does not take the test's context.
func run(t *testing.T, stdin, name string, args ...string) string {
	t.Helper()
	cmd := Command(context.Background(), name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
