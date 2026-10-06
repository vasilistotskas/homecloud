package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSameVolume(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	same, err := SameVolume(a, b)
	if err != nil || !same {
		t.Fatalf("two temp dirs: same = %v, %v; want true", same, err)
	}
	if _, err := SameVolume(a, filepath.Join(b, "missing")); err == nil {
		t.Fatal("a missing path was compared")
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
