package config

import (
	"strings"
	"testing"
)

const (
	testRoot  = `D:\Archive`
	testTools = `C:\Tools`
	// Windows has no drive-health sections.
	testDriveHealthTOML = ""
)

var (
	testDriveHealth DriveHealth
	testSmartctl    Smartctl
)

func TestDriveHealthSectionsRejected(t *testing.T) {
	for _, extra := range []string{"\n[drive_health]\nsource = \"smartctl\"\n", "\n[smartctl]\ntimeout = \"1m\"\n"} {
		if _, err := Load(writeConfig(t, validTOML+extra)); err == nil || !strings.Contains(err.Error(), "Linux only") {
			t.Errorf("%q: err = %v, want a Linux-only error", extra, err)
		}
	}
}
