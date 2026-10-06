package health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

func TestEvaluateDrive(t *testing.T) {
	cases := []struct {
		name    string
		drive   platform.DriveHealth
		state   State
		reasons []string
	}{
		{"healthy", platform.DriveHealth{PercentUsed: new(5), MediaErrors: new(uint64(0))}, StateOK, nil},
		{"no attributes", platform.DriveHealth{}, StateOK, nil},
		{
			"critical warning",
			platform.DriveHealth{Warnings: []string{"spare", "readonly"}},
			StateFailing,
			[]string{"drive reports: spare", "drive reports: readonly"},
		},
		{"media errors", platform.DriveHealth{MediaErrors: new(uint64(3))}, StateWarning, []string{"3 media errors"}},
		{"worn out", platform.DriveHealth{PercentUsed: new(100)}, StateWarning, []string{"rated endurance used up"}},
		{
			"warning beats wear",
			platform.DriveHealth{Warnings: []string{"failing"}, PercentUsed: new(120)},
			StateFailing,
			[]string{"drive reports: failing"},
		},
	}
	for _, c := range cases {
		state, reasons := EvaluateDrive(c.drive)
		if state != c.state || !cmp.Equal(reasons, c.reasons) {
			t.Errorf("%s: EvaluateDrive = %s %v, want %s %v", c.name, state, reasons, c.state, c.reasons)
		}
	}
}

func TestEvaluateArray(t *testing.T) {
	healthy := platform.RaidArray{
		Name: "md1", Level: "raid1", ArrayState: "clean", RaidDisks: 2, SyncAction: "idle",
		Members: []platform.RaidMember{
			{Name: "sda3", Drive: "sda", States: []string{"in_sync"}},
			{Name: "sdb3", Drive: "sdb", States: []string{"in_sync"}},
		},
	}
	with := func(change func(a *platform.RaidArray)) platform.RaidArray {
		a := healthy
		a.Members = []platform.RaidMember{healthy.Members[0], healthy.Members[1]}
		change(&a)
		return a
	}
	cases := []struct {
		name    string
		array   platform.RaidArray
		state   State
		reasons []string
	}{
		{"healthy", healthy, StateOK, nil},
		{"checking", with(func(a *platform.RaidArray) { a.SyncAction, a.SyncDone, a.SyncTotal = "check", 5, 10 }), StateOK, nil},
		{"mismatches are not judged", with(func(a *platform.RaidArray) { a.MismatchCount = 128 }), StateOK, nil},
		{
			"failed member",
			with(func(a *platform.RaidArray) { a.Degraded, a.Members[1].States = 1, []string{"faulty"} }),
			StateFailing,
			[]string{"lost redundancy: 1 of 2 drives missing or failed", "sdb3 has failed"},
		},
		{
			"rebuilding",
			with(func(a *platform.RaidArray) {
				a.ArrayState, a.Degraded, a.SyncAction, a.SyncDone, a.SyncTotal = "active", 1, "recover", 1024, 4096
				a.Members[1].States = []string{"spare"}
			}),
			StateFailing,
			[]string{"lost redundancy: 1 of 2 drives missing or failed", "rebuilding (recover, 25% done)"},
		},
		{
			"resync after an unclean shutdown",
			with(func(a *platform.RaidArray) { a.ArrayState, a.SyncAction = "active", "resync" }),
			StateWarning,
			[]string{"rebuilding (resync)"},
		},
		{
			"member errors",
			with(func(a *platform.RaidArray) {
				a.Members[0].States = []string{"in_sync", "write_error", "want_replacement"}
				a.Members[1].Errors = 7
			}),
			StateWarning,
			[]string{"sda3 has had a write error", "sda3 is marked for replacement", "sdb3: 7 read errors corrected"},
		},
		{"not running", with(func(a *platform.RaidArray) { a.ArrayState = "inactive" }), StateFailing, []string{"array is not running (inactive)"}},
		{"read-only", with(func(a *platform.RaidArray) { a.ArrayState = "readonly" }), StateWarning, []string{"array is readonly"}},
		{"unknown state", with(func(a *platform.RaidArray) { a.ArrayState = "melting" }), StateWarning, []string{`array reports an unknown state "melting"`}},
	}
	for _, c := range cases {
		state, reasons := EvaluateArray(c.array)
		if state != c.state || !cmp.Equal(reasons, c.reasons) {
			t.Errorf("%s: EvaluateArray = %s %v, want %s %v", c.name, state, reasons, c.state, c.reasons)
		}
	}
}

func TestCheck(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	storage := platform.Storage{
		Drives: []platform.Drive{{Name: "sda", Device: "/dev/sda"}, {Name: "sdb"}},
		Arrays: []platform.RaidArray{{Name: "md1", Level: "raid1", ArrayState: "clean", RaidDisks: 2, SyncAction: "idle"}},
	}
	read := func(_ context.Context, d platform.Drive) (platform.DriveHealth, error) {
		if d.Device == "" {
			return platform.DriveHealth{}, errors.New("no device node here")
		}
		return platform.DriveHealth{Protocol: "ATA", MediaErrors: new(uint64(3))}, nil
	}
	r := Check(t.Context(), func() (platform.Storage, error) { return storage, nil }, read, now)
	// The warning sda reports outranks sdb being unreadable, so it alerts.
	if r.State != StateWarning || !r.CheckedAt.Equal(now) {
		t.Fatalf("state = %s at %v", r.State, r.CheckedAt)
	}
	if want := []string{"sda: 3 media errors", "sdb: no device node here"}; !cmp.Equal(r.Reasons, want) {
		t.Fatalf("reasons = %v, want %v", r.Reasons, want)
	}
	if len(r.Drives) != 2 || r.Drives[0].Health == nil || r.Drives[1].Health != nil || r.Drives[1].State != StateUnavailable {
		t.Fatalf("drives = %+v", r.Drives)
	}
	if len(r.Arrays) != 1 || r.Arrays[0].State != StateOK || r.Arrays[0].Name != "md1" {
		t.Fatalf("arrays = %+v", r.Arrays)
	}

	// Only an unreadable drive: unavailable, never healthy.
	storage.Drives = storage.Drives[1:]
	if r := Check(t.Context(), func() (platform.Storage, error) { return storage, nil }, read, now); r.State != StateUnavailable {
		t.Fatalf("unreadable only = %s", r.State)
	}
}

func TestCheckWithoutStorage(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	never := func(context.Context, platform.Drive) (platform.DriveHealth, error) {
		t.Fatal("no drive to read")
		return platform.DriveHealth{}, nil
	}
	r := Check(t.Context(), func() (platform.Storage, error) { return platform.Storage{}, errors.New("not a block device") }, never, now)
	if r.State != StateUnavailable || r.Reasons[0] != "not a block device" || len(r.Drives) != 0 {
		t.Fatalf("report = %+v", r)
	}
	r = Check(t.Context(), func() (platform.Storage, error) { return platform.Storage{}, nil }, never, now)
	if r.State != StateUnavailable || r.Reasons[0] != "no drive found under the archive" {
		t.Fatalf("report = %+v", r)
	}
}

func TestReaderFor(t *testing.T) {
	// smartctl is chosen by configuration; its reader refuses a drive without
	// a device node before running anything.
	cfg := &config.Config{DriveHealth: config.DriveHealth{Source: config.DriveHealthSmartctl}}
	if _, err := ReaderFor(cfg)(t.Context(), platform.Drive{Name: "sda"}); err == nil || !strings.Contains(err.Error(), "devices:") {
		t.Fatalf("smartctl reader = %v", err)
	}
}
