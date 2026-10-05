//go:build windows

package health

import (
	"regexp"
	"strings"
	"testing"
)

var guidName = regexp.MustCompile(`^\\\\\?\\Volume\{[0-9a-fA-F-]{36}\}\\$`)

func TestVolumeGUID(t *testing.T) {
	got, err := VolumeGUID(t.TempDir())
	if err != nil {
		t.Fatalf("VolumeGUID: %v", err)
	}
	if !guidName.MatchString(got) {
		t.Fatalf("VolumeGUID = %q, want \\\\?\\Volume{GUID}\\ form", got)
	}
	if err := CheckVolume(t.TempDir(), strings.ToUpper(got)); err != nil {
		t.Fatalf("CheckVolume with own GUID (case-insensitive): %v", err)
	}
}

func TestCheckVolumeMismatch(t *testing.T) {
	err := CheckVolume(t.TempDir(), `\\?\Volume{00000000-0000-0000-0000-000000000000}\`)
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("want mismatch error, got %v", err)
	}
}

func TestFreeSpace(t *testing.T) {
	s, err := DiskSpace(t.TempDir())
	if err != nil {
		t.Fatalf("DiskSpace: %v", err)
	}
	if s.Total == 0 || s.Free > s.Total {
		t.Fatalf("implausible space %+v", s)
	}
	if _, err := CheckFreeSpace(t.TempDir(), 99.999); err == nil && s.FreePercent() < 99.999 {
		t.Fatal("want error when below minimum")
	}
}

func TestFreePercent(t *testing.T) {
	tests := []struct {
		s    Space
		want float64
	}{
		{Space{Free: 25, Total: 100}, 25},
		{Space{Free: 0, Total: 100}, 0},
		{Space{}, 0},
	}
	for _, tt := range tests {
		if got := tt.s.FreePercent(); got != tt.want {
			t.Errorf("%+v.FreePercent() = %v, want %v", tt.s, got, tt.want)
		}
	}
}
