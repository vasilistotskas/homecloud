package smartctl

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// testdata/captured holds real smartctl 7.5-2~bpo13+1 output (Debian
// trixie-backports, in Docker); testdata/constructed holds drive reports
// written by generate.mjs from the keys smartmontools 7.5 writes.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDrives(t *testing.T) {
	cases := []struct {
		file string
		want platform.DriveHealth
	}{
		{"constructed/nvme-healthy.json", platform.DriveHealth{
			Model: "Samsung SSD 990 PRO 2TB", Serial: "S7DNNJ0X123456A", Protocol: "NVMe",
			TemperatureCelsius: new(41), PercentUsed: new(3), AvailableSparePercent: new(100),
			MediaErrors: new(uint64(0)), PowerOnHours: new(uint64(9587)),
		}},
		{"constructed/nvme-critical-warning.json", platform.DriveHealth{
			Model: "Samsung SSD 990 PRO 2TB", Serial: "S7DNNJ0X123456A", Protocol: "NVMe",
			Warnings:           []string{"spare", "degraded"},
			TemperatureCelsius: new(41), PercentUsed: new(3), AvailableSparePercent: new(100),
			MediaErrors: new(uint64(12)), PowerOnHours: new(uint64(9587)),
		}},
		{"constructed/ata-hdd-healthy.json", platform.DriveHealth{
			Model: "ST4000VN006-3CW104", Serial: "ZW6123AB", Protocol: "ATA",
			TemperatureCelsius: new(34), MediaErrors: new(uint64(0)), PowerOnHours: new(uint64(1200)),
		}},
		{"constructed/ata-hdd-bad-sectors.json", platform.DriveHealth{
			Model: "ST4000VN006-3CW104", Serial: "ZW6123AB", Protocol: "ATA",
			TemperatureCelsius: new(34), MediaErrors: new(uint64(64)), PowerOnHours: new(uint64(1200)),
		}},
		{"constructed/ata-hdd-failing.json", platform.DriveHealth{
			Model: "ST4000VN006-3CW104", Serial: "ZW6123AB", Protocol: "ATA", Warnings: []string{"failing"},
			TemperatureCelsius: new(34), MediaErrors: new(uint64(1304)), PowerOnHours: new(uint64(1200)),
		}},
		{"constructed/ata-ssd-healthy.json", platform.DriveHealth{
			Model: "Samsung SSD 870 EVO 1TB", Serial: "S6PUNX0T654321B", Protocol: "ATA",
			TemperatureCelsius: new(32), PercentUsed: new(7), AvailableSparePercent: new(100),
			MediaErrors: new(uint64(0)), PowerOnHours: new(uint64(14002)),
		}},
	}
	for _, tc := range cases {
		got, err := parse(fixture(t, tc.file))
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.file, diff)
		}
	}
}

// A pre-failure attribute at its threshold is a failure prediction even when
// the drive's own SMART status still says it passed.
func TestParsePrefailAttributeAtThreshold(t *testing.T) {
	b := bytes.Replace(fixture(t, "constructed/ata-hdd-healthy.json"), []byte(`"exit_status":0`), []byte(`"exit_status":16`), 1)
	got, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(got.Warnings, []string{"failing"}) {
		t.Fatalf("warnings = %v, want [failing]", got.Warnings)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name        string
		out         []byte
		unsupported bool
		wantMsg     string
	}{
		{"virtual SCSI disk", fixture(t, "captured/scsi-virtual-disk.json"), true, "SCSI drives are not read"},
		{"permission denied", fixture(t, "captured/permission-denied.json"), false, "Permission denied"},
		{"no such device", fixture(t, "captured/no-such-device.json"), false, "Unable to detect device type"},
		{"not JSON", []byte("smartctl: unrecognized option"), false, "decode smartctl output"},
		{"other JSON format", bytes.Replace(fixture(t, "constructed/nvme-healthy.json"),
			[]byte(`"json_format_version":[1,0]`), []byte(`"json_format_version":[2,0]`), 1), false, "is not 1.x"},
		{"no SMART status", bytes.Replace(fixture(t, "constructed/ata-hdd-healthy.json"),
			[]byte(`"smart_status":{"passed":true}`), []byte(`"smart_status_absent":{}`), 1), true, "no SMART status"},
		{"no NVMe log", bytes.Replace(fixture(t, "constructed/nvme-healthy.json"),
			[]byte(`"nvme_smart_health_information_log"`), []byte(`"nvme_log_absent"`), 1), false, "no NVMe health log"},
		{"128-bit media errors", bytes.Replace(fixture(t, "constructed/nvme-healthy.json"),
			[]byte(`"media_errors":0`), []byte(`"media_errors":18446744073709551616`), 1), false, "decode smartctl output"},
	}
	for _, tc := range cases {
		_, err := parse(tc.out)
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		if errors.Is(err, platform.ErrHealthUnsupported) != tc.unsupported || !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: err = %v (unsupported %v), want %q (unsupported %v)",
				tc.name, err, errors.Is(err, platform.ErrHealthUnsupported), tc.wantMsg, tc.unsupported)
		}
	}
}

func TestReadWithoutDeviceNode(t *testing.T) {
	_, err := Read(t.Context(), config.Smartctl{}, platform.Drive{Name: "sda"})
	if !errors.Is(err, platform.ErrHealthUnsupported) || !strings.Contains(err.Error(), "devices:") {
		t.Fatalf("err = %v, want unsupported with how to pass the drive", err)
	}
}

func TestVersionPattern(t *testing.T) {
	// The first line of smartctl 7.5-2~bpo13+1's --version.
	line := "smartctl 7.5 2025-04-30 r5714 [x86_64-linux-6.18.33.2-microsoft-standard-WSL2] (local build)"
	if m := versionPattern.FindStringSubmatch(line); m == nil || m[1] != "7.5" {
		t.Fatalf("version = %v", m)
	}
}
