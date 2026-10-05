package platform

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ownVolumeID returns the identity of the test temp dir, skipping when the test
// environment (e.g. a container overlay) has no stable volume identity.
func ownVolumeID(t *testing.T) string {
	t.Helper()
	id, err := VolumeID(t.TempDir())
	if errors.Is(err, ErrNoVolumeIdentity) {
		t.Skipf("test temp dir has no volume identity in this environment: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestVolumeIDIsValidAndStable(t *testing.T) {
	got := ownVolumeID(t)
	if err := ValidateVolumeID(got); err != nil {
		t.Fatalf("VolumeID returned an id its own validator rejects: %v", err)
	}
	if err := CheckVolume(t.TempDir(), strings.ToUpper(got)); err != nil {
		t.Fatalf("CheckVolume with own id (case-insensitive): %v", err)
	}
}

func TestCheckVolumeMismatch(t *testing.T) {
	own := ownVolumeID(t)
	err := CheckVolume(t.TempDir(), own+"-other")
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("want mismatch error, got %v", err)
	}
}

func TestDiskSpace(t *testing.T) {
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

func TestAcquireInstanceIsExclusive(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // Linux lock location; unused on Windows
	name := fmt.Sprintf("homecloud-test-%d", time.Now().UnixNano())
	l, err := AcquireInstance(name)
	if err != nil {
		t.Fatalf("first AcquireInstance: %v", err)
	}
	if _, secondErr := AcquireInstance(name); !errors.Is(secondErr, ErrAlreadyRunning) {
		t.Fatalf("second AcquireInstance: want ErrAlreadyRunning, got %v", secondErr)
	}
	if releaseErr := l.Release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	l2, err := AcquireInstance(name)
	if err != nil {
		t.Fatalf("AcquireInstance after release: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatal(err)
	}
}
