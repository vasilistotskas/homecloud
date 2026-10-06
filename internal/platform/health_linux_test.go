package platform

import (
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
	got, err := parseUdisksNVMe(props, attrs)
	if err != nil {
		t.Fatal(err)
	}
	want := DriveHealth{
		Protocol: "NVMe", Warnings: []string{"temperature"},
		TemperatureCelsius: new(45), AvailableSparePercent: new(100), PercentUsed: new(7),
		MediaErrors: new(uint64(0)), PowerOnHours: new(uint64(1200)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("health (-want +got):\n%s", diff)
	}

	props["SmartUpdated"] = dbus.MakeVariant(uint64(0))
	if _, err := parseUdisksNVMe(props, attrs); !errors.Is(err, ErrHealthUnsupported) {
		t.Fatalf("never-read SMART = %v, want ErrHealthUnsupported", err)
	}
}

func TestParseUdisksATA(t *testing.T) {
	props := vmap(map[string]any{
		"SmartSupported": true, "SmartEnabled": true, "SmartUpdated": uint64(1), "SmartFailing": true,
		"SmartTemperature": 311.15, "SmartPowerOnSeconds": uint64(7200), "SmartNumBadSectors": int64(-1),
	})
	got, err := parseUdisksATA(props)
	if err != nil {
		t.Fatal(err)
	}
	want := DriveHealth{
		Protocol: "ATA", Warnings: []string{"failing"},
		TemperatureCelsius: new(38), PowerOnHours: new(uint64(2)),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("health (-want +got):\n%s", diff)
	}

	props["SmartEnabled"] = dbus.MakeVariant(false)
	if _, err := parseUdisksATA(props); !errors.Is(err, ErrHealthUnsupported) {
		t.Fatalf("SMART disabled = %v, want ErrHealthUnsupported", err)
	}
}

// TestReadDriveHealthOfThisMachine needs udisks2 and a physical drive under
// the temp directory; containers and CI runners usually have neither.
func TestReadDriveHealthOfThisMachine(t *testing.T) {
	s, err := StorageOf(t.TempDir())
	if errors.Is(err, ErrHealthUnsupported) {
		t.Skipf("the temp directory is not on a drive here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range s.Drives {
		h, err := ReadDriveHealth(t.Context(), d)
		if err != nil {
			t.Skipf("udisks2 cannot report %s here: %v", d.Name, err)
		}
		t.Logf("%s: %+v", d.Name, h)
	}
}
