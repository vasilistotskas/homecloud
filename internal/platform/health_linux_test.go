package platform

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/google/go-cmp/cmp"
)

func vmap(kv map[string]any) map[string]dbus.Variant {
	out := map[string]dbus.Variant{}
	for k, v := range kv {
		out[k] = dbus.MakeVariant(v)
	}
	return out
}

func TestParseUdisksNVMe(t *testing.T) {
	props := vmap(map[string]any{
		"SmartUpdated": uint64(1), "SmartCriticalWarning": []string{"temperature"},
		"SmartPowerOnHours": uint64(1200), "SmartTemperature": uint16(318),
	})
	attrs := vmap(map[string]any{"avail_spare": byte(100), "percent_used": byte(7), "media_errors": uint64(0)})
	got, err := parseUdisksNVMe("WD Blue SN580", props, attrs)
	if err != nil {
		t.Fatal(err)
	}
	want := DriveHealth{
		Model: "WD Blue SN580", Protocol: "NVMe", Warnings: []string{"temperature"},
		TemperatureCelsius: ptr(45), AvailableSparePercent: ptr(100), PercentUsed: ptr(7),
		MediaErrors: ptr(uint64(0)), PowerOnHours: ptr(uint64(1200)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("health (-want +got):\n%s", diff)
	}

	props["SmartUpdated"] = dbus.MakeVariant(uint64(0))
	if _, err := parseUdisksNVMe("x", props, attrs); !errors.Is(err, ErrHealthUnsupported) {
		t.Fatalf("never-read SMART = %v, want ErrHealthUnsupported", err)
	}
}

func TestParseUdisksATA(t *testing.T) {
	props := vmap(map[string]any{
		"SmartSupported": true, "SmartEnabled": true, "SmartUpdated": uint64(1), "SmartFailing": true,
		"SmartTemperature": 311.15, "SmartPowerOnSeconds": uint64(7200), "SmartNumBadSectors": int64(-1),
	})
	got, err := parseUdisksATA("ST2000DM008", props)
	if err != nil {
		t.Fatal(err)
	}
	want := DriveHealth{
		Model: "ST2000DM008", Protocol: "ATA", Warnings: []string{"failing"},
		TemperatureCelsius: ptr(38), PowerOnHours: ptr(uint64(2)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("health (-want +got):\n%s", diff)
	}

	props["SmartEnabled"] = dbus.MakeVariant(false)
	if _, err := parseUdisksATA("x", props); !errors.Is(err, ErrHealthUnsupported) {
		t.Fatalf("SMART disabled = %v, want ErrHealthUnsupported", err)
	}
}

// TestDriveHealthOfThisMachine needs udisks2 and a physical drive; containers
// and CI runners usually have neither.
func TestDriveHealthOfThisMachine(t *testing.T) {
	h, err := DriveHealthOf(context.Background(), t.TempDir())
	if errors.Is(err, ErrHealthUnsupported) || errors.Is(err, ErrNoVolumeIdentity) {
		t.Skipf("drive health unsupported here: %v", err)
	}
	if err != nil {
		t.Skipf("udisks2 is not reachable here: %v", err)
	}
	t.Logf("%+v", h)
}
