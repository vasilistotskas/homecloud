package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sys/unix"
)

// fakeSysfs builds a sysfs tree in a temporary directory: files with their
// contents, and symbolic links (relative to the link's directory, as the
// kernel makes them) for entries whose value starts with "->".
func fakeSysfs(t *testing.T, entries map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, value := range entries {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(value, "->"); ok {
			if err := os.Symlink(filepath.FromSlash(target), full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if value == "/" {
			if err := os.MkdirAll(full, 0o750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ugosSysfs is how UGOS Pro lays out a storage pool: an ext4 volume on an
// LVM logical volume (dm-0) on an md raid1 (md1) of the third partition of
// each drive.
func ugosSysfs(md map[string]string) map[string]string {
	e := map[string]string{
		"dev/block/254:0": "->../../devices/virtual/block/dm-0",

		"devices/virtual/block/dm-0/dev":          "254:0",
		"devices/virtual/block/dm-0/dm/name":      "ug_7F2A_pool1-volume1",
		"devices/virtual/block/dm-0/slaves/md1":   "->../../md1",
		"devices/virtual/block/md1/dev":           "9:1",
		"devices/virtual/block/md1/slaves/sda3":   "->../../../../pci0/ata1/block/sda/sda3",
		"devices/virtual/block/md1/slaves/sdb3":   "->../../../../pci0/ata2/block/sdb/sdb3",
		"devices/virtual/block/md1/md/level":      "raid1",
		"devices/virtual/block/md1/md/raid_disks": "2",

		"devices/virtual/block/md1/md/dev-sda3/block":  "->../../../../../pci0/ata1/block/sda/sda3",
		"devices/virtual/block/md1/md/dev-sda3/state":  "in_sync",
		"devices/virtual/block/md1/md/dev-sda3/errors": "0",
		"devices/virtual/block/md1/md/dev-sdb3/block":  "->../../../../../pci0/ata2/block/sdb/sdb3",
		"devices/virtual/block/md1/md/dev-sdb3/errors": "0",

		"devices/pci0/ata1/block/sda/dev":            "8:0",
		"devices/pci0/ata1/block/sda/sda3/dev":       "8:3",
		"devices/pci0/ata1/block/sda/sda3/partition": "3",
		"devices/pci0/ata2/block/sdb/dev":            "8:16",
		"devices/pci0/ata2/block/sdb/sdb3/dev":       "8:19",
		"devices/pci0/ata2/block/sdb/sdb3/partition": "3",

		"class/block/dm-0": "->../../devices/virtual/block/dm-0",
		"class/block/md1":  "->../../devices/virtual/block/md1",
		"class/block/sda":  "->../../devices/pci0/ata1/block/sda",
		"class/block/sdb":  "->../../devices/pci0/ata2/block/sdb",
	}
	for k, v := range md {
		e[k] = v
	}
	return e
}

func healthyMD() map[string]string {
	return map[string]string{
		"devices/virtual/block/md1/md/array_state":    "clean",
		"devices/virtual/block/md1/md/degraded":       "0",
		"devices/virtual/block/md1/md/sync_action":    "idle",
		"devices/virtual/block/md1/md/sync_completed": "none",
		"devices/virtual/block/md1/md/mismatch_cnt":   "0",
		"devices/virtual/block/md1/md/dev-sdb3/state": "in_sync",
	}
}

func TestStorageOfUGOSPool(t *testing.T) {
	sys := fakeSysfs(t, ugosSysfs(healthyMD()))
	got, err := storageOf(sys, t.TempDir(), mount{dev: unix.Mkdev(254, 0), fsType: "ext4", source: "/dev/mapper/ug_7F2A_pool1-volume1"})
	if err != nil {
		t.Fatal(err)
	}
	want := Storage{
		// No device nodes here: a container the drives were not passed to.
		Drives: []Drive{{Name: "sda"}, {Name: "sdb"}},
		Arrays: []RaidArray{{
			Name: "md1", Level: "raid1", ArrayState: "clean", RaidDisks: 2, SyncAction: "idle",
			Members: []RaidMember{
				{Name: "sda3", Drive: "sda", States: []string{"in_sync"}},
				{Name: "sdb3", Drive: "sdb", States: []string{"in_sync"}},
			},
		}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("storage (-want +got):\n%s", diff)
	}
}

func TestStorageOfRebuildingArray(t *testing.T) {
	md := map[string]string{
		"devices/virtual/block/md1/md/array_state":    "active",
		"devices/virtual/block/md1/md/degraded":       "1",
		"devices/virtual/block/md1/md/sync_action":    "recover",
		"devices/virtual/block/md1/md/sync_completed": "1024 / 4096",
		"devices/virtual/block/md1/md/mismatch_cnt":   "128",
		"devices/virtual/block/md1/md/dev-sdb3/state": "spare,write_error",
	}
	got, err := storageOf(fakeSysfs(t, ugosSysfs(md)), t.TempDir(), mount{dev: unix.Mkdev(254, 0), fsType: "ext4"})
	if err != nil {
		t.Fatal(err)
	}
	a := got.Arrays[0]
	if a.Degraded != 1 || a.SyncAction != "recover" || a.SyncDone != 1024 || a.SyncTotal != 4096 || a.MismatchCount != 128 {
		t.Fatalf("array = %+v", a)
	}
	if !cmp.Equal(a.Members[1].States, []string{"spare", "write_error"}) {
		t.Fatalf("member states = %v", a.Members[1].States)
	}
}

func TestStorageOfPlainPartition(t *testing.T) {
	sys := fakeSysfs(t, map[string]string{
		"dev/block/259:1":                                     "->../../devices/pci0/nvme/nvme0/nvme0n1/nvme0n1p1",
		"devices/pci0/nvme/nvme0/nvme0n1/dev":                 "259:0",
		"devices/pci0/nvme/nvme0/nvme0n1/nvme0n1p1/dev":       "259:1",
		"devices/pci0/nvme/nvme0/nvme0n1/nvme0n1p1/partition": "1",
	})
	got, err := storageOf(sys, t.TempDir(), mount{dev: unix.Mkdev(259, 1), fsType: "ext4", source: "/dev/nvme0n1p1"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Storage{Drives: []Drive{{Name: "nvme0n1"}}}, got); diff != "" {
		t.Fatalf("storage (-want +got):\n%s", diff)
	}
}

func TestStorageOfMultiDeviceBtrfs(t *testing.T) {
	// btrfs reports an anonymous device number; the mount names one of its
	// devices and /sys/fs/btrfs/<fsid>/devices lists all of them.
	sys := fakeSysfs(t, map[string]string{
		"devices/pci0/ata1/block/sdc/dev":            "8:32",
		"devices/pci0/ata1/block/sdc/sdc1/dev":       "8:33",
		"devices/pci0/ata1/block/sdc/sdc1/partition": "1",
		"devices/pci0/ata2/block/sdd/dev":            "8:48",
		"devices/pci0/ata2/block/sdd/sdd1/dev":       "8:49",
		"devices/pci0/ata2/block/sdd/sdd1/partition": "1",
		"class/block/sdc1":                           "->../../devices/pci0/ata1/block/sdc/sdc1",
		"class/block/sdd1":                           "->../../devices/pci0/ata2/block/sdd/sdd1",
		"fs/btrfs/0d1f/devices/sdc1":                 "->../../../../devices/pci0/ata1/block/sdc/sdc1",
		"fs/btrfs/0d1f/devices/sdd1":                 "->../../../../devices/pci0/ata2/block/sdd/sdd1",
		"fs/btrfs/9e2a/devices/sde1":                 "->../../../../devices/pci0/ata3/block/sde/sde1",
	})
	got, err := storageOf(sys, t.TempDir(), mount{dev: unix.Mkdev(0, 45), fsType: "btrfs", source: "/dev/sdd1"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Storage{Drives: []Drive{{Name: "sdc"}, {Name: "sdd"}}}, got); diff != "" {
		t.Fatalf("storage (-want +got):\n%s", diff)
	}
}

func TestStorageOfBtrfsOnDeviceMapper(t *testing.T) {
	// UGOS Pro's btrfs volumes: the mount source is the host's /dev/mapper
	// path, which a container does not have; sysfs knows the name.
	sys := fakeSysfs(t, map[string]string{
		"devices/virtual/block/dm-2/dev":             "254:2",
		"devices/virtual/block/dm-2/dm/name":         "ug_7F2A_pool1-volume1",
		"devices/virtual/block/dm-2/slaves/sda3":     "->../../../../pci0/ata1/block/sda/sda3",
		"devices/pci0/ata1/block/sda/dev":            "8:0",
		"devices/pci0/ata1/block/sda/sda3/dev":       "8:3",
		"devices/pci0/ata1/block/sda/sda3/partition": "3",
		"devices/virtual/block/dm-5/dev":             "254:5",
		"devices/virtual/block/dm-5/dm/name":         "other",
		"class/block/dm-2":                           "->../../devices/virtual/block/dm-2",
		"class/block/dm-5":                           "->../../devices/virtual/block/dm-5",
		"class/block/sda":                            "->../../devices/pci0/ata1/block/sda",
		"fs/btrfs/0d1f/devices/dm-2":                 "->../../../../devices/virtual/block/dm-2",
	})
	got, err := storageOf(sys, t.TempDir(), mount{dev: unix.Mkdev(0, 51), fsType: "btrfs", source: "/dev/mapper/ug_7F2A_pool1-volume1"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Storage{Drives: []Drive{{Name: "sda"}}}, got); diff != "" {
		t.Fatalf("storage (-want +got):\n%s", diff)
	}
}

func TestStorageOfErrors(t *testing.T) {
	sys := fakeSysfs(t, ugosSysfs(healthyMD()))
	dev := t.TempDir()
	cases := []struct {
		name string
		m    mount
	}{
		{"unknown device number", mount{dev: unix.Mkdev(254, 9), fsType: "ext4", source: "/dev/dm-9"}},
		{"unknown mapper name", mount{dev: unix.Mkdev(0, 51), fsType: "btrfs", source: "/dev/mapper/nope"}},
		{"btrfs device in no file system", mount{dev: unix.Mkdev(0, 51), fsType: "btrfs", source: "/dev/sda"}},
	}
	for _, tc := range cases {
		if _, err := storageOf(sys, dev, tc.m); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}

	// An md attribute every array has must be there.
	broken := ugosSysfs(healthyMD())
	delete(broken, "devices/virtual/block/md1/md/raid_disks")
	if _, err := storageOf(fakeSysfs(t, broken), dev, mount{dev: unix.Mkdev(254, 0), fsType: "ext4"}); err == nil {
		t.Error("an array without raid_disks was accepted")
	}
}

func TestStorageOfLoopingSlaves(t *testing.T) {
	sys := fakeSysfs(t, map[string]string{
		"dev/block/254:0":                        "->../../devices/virtual/block/dm-0",
		"devices/virtual/block/dm-0/dev":         "254:0",
		"devices/virtual/block/dm-0/slaves/dm-1": "->../../dm-1",
		"devices/virtual/block/dm-1/dev":         "254:1",
		"devices/virtual/block/dm-1/slaves/dm-0": "->../../dm-0",
	})
	// A cycle cannot occur in a real kernel; the walk must still end.
	got, err := storageOf(sys, t.TempDir(), mount{dev: unix.Mkdev(254, 0), fsType: "ext4"})
	if err != nil || len(got.Drives) != 0 {
		t.Fatalf("storage = %+v, %v", got, err)
	}
}

func TestParseSyncCompleted(t *testing.T) {
	for _, tc := range []struct {
		in          string
		done, total uint64
		ok          bool
	}{
		{"none", 0, 0, true},
		{"delayed", 0, 0, true},
		{"0 / 260096", 0, 260096, true},
		{"7 / x", 0, 0, false},
		{"7/8", 0, 0, false},
	} {
		done, total, err := parseSyncCompleted(tc.in)
		if (err == nil) != tc.ok || done != tc.done || total != tc.total {
			t.Errorf("parseSyncCompleted(%q) = %d, %d, %v", tc.in, done, total, err)
		}
	}
}

func TestBestNode(t *testing.T) {
	if got := bestNode([]string{"/dev/disk/by-id/ata-X", "/dev/sda", "/dev/block/8:0"}, "sda"); got != "/dev/sda" {
		t.Errorf("named node = %q", got)
	}
	if got := bestNode([]string{"/dev/disk/by-id/ata-X", "/dev/block/8:0"}, "sda"); got != "/dev/block/8:0" {
		t.Errorf("first node = %q", got)
	}
	if got := bestNode(nil, "sda"); got != "" {
		t.Errorf("no node = %q", got)
	}
}

// TestBlockNodes needs mknod, which only root has (the Docker test run).
func TestBlockNodes(t *testing.T) {
	dev := t.TempDir()
	node := filepath.Join(dev, "disk", "by-id", "ata-TEST")
	if err := os.MkdirAll(filepath.Dir(node), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(node, unix.S_IFBLK|0o600, int(unix.Mkdev(8, 16))); errors.Is(err, unix.EPERM) { //nolint:gosec // device 8:16 fits an int
		t.Skip("mknod needs root")
	} else if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("disk/by-id/ata-TEST", filepath.Join(dev, "sdb")); err != nil {
		t.Fatal(err)
	}
	nodes, err := blockNodes(dev)
	if err != nil {
		t.Fatal(err)
	}
	got := nodes[unix.Mkdev(8, 16)]
	want := []string{filepath.Join(dev, "disk", "by-id", "ata-TEST"), filepath.Join(dev, "sdb")}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("nodes (-want +got):\n%s", diff)
	}
	if bestNode(got, "sdb") != filepath.Join(dev, "sdb") {
		t.Fatal("the node named after the drive must win")
	}
}
