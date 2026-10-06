package config

import (
	"strings"
	"testing"
	"time"
)

const (
	testRoot  = `/srv/homecloud`
	testTools = `/opt/tools`

	testDriveHealthTOML = `
[drive_health]
source = "smartctl"

[smartctl]
binary = "/opt/tools/smartctl"
version = "7.5"
timeout = "1m"
`
)

var (
	testDriveHealth = DriveHealth{Source: DriveHealthSmartctl}
	testSmartctl    = Smartctl{Binary: "/opt/tools/smartctl", Version: "7.5", Timeout: Duration(time.Minute)}
)

func TestDriveHealthSources(t *testing.T) {
	base := strings.Replace(validTOML, testDriveHealthTOML, "", 1)
	cases := []struct {
		name, extra, wantErr string
	}{
		{"udisks", "\n[drive_health]\nsource = \"udisks\"\n", ""},
		{"no source", "", "drive_health.source must be"},
		{"unknown source", "\n[drive_health]\nsource = \"smart\"\n", "drive_health.source must be"},
		{"udisks with smartctl", "\n[drive_health]\nsource = \"udisks\"\n\n[smartctl]\ntimeout = \"1m\"\n", "[smartctl] is used only"},
		{"smartctl without its section", "\n[drive_health]\nsource = \"smartctl\"\n", "smartctl.binary must be"},
		{"patch version", strings.Replace(testDriveHealthTOML, `"7.5"`, `"7.5.0"`, 1), "smartctl.version must be"},
		{"short timeout", strings.Replace(testDriveHealthTOML, `"1m"`, `"1s"`, 1), "smartctl.timeout must be"},
	}
	for _, tc := range cases {
		_, err := Load(writeConfig(t, base+tc.extra))
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}
