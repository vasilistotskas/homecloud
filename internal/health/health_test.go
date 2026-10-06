package health

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

func TestEvaluate(t *testing.T) {
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
		state, reasons := Evaluate(c.drive)
		if state != c.state || !cmp.Equal(reasons, c.reasons) {
			t.Errorf("%s: Evaluate = %s %v, want %s %v", c.name, state, reasons, c.state, c.reasons)
		}
	}
}

func TestCheckUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	r := Check(func() (platform.DriveHealth, error) { return platform.DriveHealth{}, errors.New("not NVMe") }, now)
	if r.State != StateUnavailable || r.Drive != nil || r.Reasons[0] != "not NVMe" || !r.CheckedAt.Equal(now) {
		t.Fatalf("report = %+v", r)
	}
}
