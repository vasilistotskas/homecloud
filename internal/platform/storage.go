package platform

// Storage is the physical storage under a path: every drive holding the
// file system and, on Linux, the md software RAID arrays between them.
type Storage struct {
	// Drives are sorted by name.
	Drives []Drive
	// Arrays are sorted by name.
	Arrays []RaidArray
}

// Drive is one physical drive holding (part of) a file system.
type Drive struct {
	// Name is the operating system's name for the drive: sda or nvme0n1 on
	// Linux, PhysicalDrive1 on Windows.
	Name string
	// Device opens the drive here: /dev/sda, \\.\PhysicalDrive1. It is ""
	// when this environment has no device node for it, for example a
	// container the drive was not passed to.
	Device string
}

// RaidArray is a Linux md array, as the kernel reports it in
// /sys/block/<md>/md (docs.kernel.org/admin-guide/md.html).
type RaidArray struct {
	Name       string // md127
	Level      string // raid1
	ArrayState string // clean, active, inactive, …
	RaidDisks  int    // members of the complete array
	// Degraded counts the members the array is missing; arrays without
	// redundancy (raid0, linear) report none.
	Degraded int
	// SyncAction is idle, resync, recover, check, repair, reshape or frozen;
	// "" for arrays without redundancy.
	SyncAction string
	// SyncDone and SyncTotal are the sectors of the running sync action;
	// both 0 when none runs.
	SyncDone, SyncTotal uint64
	// MismatchCount is what the last check or repair found. On raid1 it can
	// be non-zero without damage (md(4)), so it is reported, not judged.
	MismatchCount uint64
	Members       []RaidMember
}

// RaidMember is one device of an md array.
type RaidMember struct {
	Name  string // sda3
	Drive string // sda, the Drive it is on
	// States are md's flags for it: in_sync, faulty, spare, write_error,
	// want_replacement, blocked, …
	States []string
	// Errors approximately counts read errors the array corrected without
	// evicting the device.
	Errors uint64
}

// nvmeCriticalWarnings names the bits of the NVMe SMART / Health log's
// Critical Warning byte (NVMe Base Specification, Get Log Page 02h), using
// the names udisks reports for the same bits.
var nvmeCriticalWarnings = []string{"spare", "temperature", "degraded", "readonly", "volatile_mem", "pmr_readonly"}

// NVMeWarnings names the bits set in an NVMe Critical Warning byte.
func NVMeWarnings(b byte) []string {
	var out []string
	for i, name := range nvmeCriticalWarnings {
		if b&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	if b>>len(nvmeCriticalWarnings) != 0 {
		out = append(out, "unknown")
	}
	return out
}
