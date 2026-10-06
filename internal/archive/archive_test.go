package archive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestInitThenCheck(t *testing.T) {
	root := t.TempDir()
	id, err := Init(root, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateID(id); err != nil {
		t.Fatalf("Init returned an id ValidateID rejects: %v", err)
	}
	if err := Check(root, id); err != nil {
		t.Fatalf("Check own archive: %v", err)
	}
	if got, err := ID(root); err != nil || got != id {
		t.Fatalf("ID = %q, %v; want %q", got, err, id)
	}
}

func TestInitNeverReplacesAMarker(t *testing.T) {
	root := t.TempDir()
	id, err := Init(root, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root, t0); err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("second Init = %v, want an error naming %s", err, id)
	}
	if got, _ := ID(root); got != id {
		t.Fatalf("marker changed to %q", got)
	}
}

func TestInitNeedsAnExistingFolder(t *testing.T) {
	if _, err := Init(filepath.Join(t.TempDir(), "missing"), t0); err == nil {
		t.Fatal("Init on a missing folder succeeded")
	}
}

func TestCheckFailsClosed(t *testing.T) {
	marked := t.TempDir()
	id, err := Init(marked, t0)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	otherID, err := Init(other, t0)
	if err != nil {
		t.Fatal(err)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, MarkerFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Check(t.TempDir(), id); !errors.Is(err, ErrNotMarked) {
		t.Errorf("empty folder (unmounted drive): %v, want ErrNotMarked", err)
	}
	if err := Check(filepath.Join(t.TempDir(), "gone"), id); !errors.Is(err, ErrNotMarked) {
		t.Errorf("missing folder: %v, want ErrNotMarked", err)
	}
	if err := Check(other, id); err == nil || !strings.Contains(err.Error(), otherID) {
		t.Errorf("another archive: %v, want a mismatch naming %s", err, otherID)
	}
	if err := Check(broken, id); err == nil {
		t.Error("unreadable marker accepted")
	}
}

func TestValidateID(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-uuid",
		"0A1B2C3D-1111-4222-8333-444455556666", // not lowercase
		"0a1b2c3d-1111-1222-8333-444455556666", // version 1
		`\\?\Volume{0a1b2c3d-1111-2222-3333-444455556666}\`,
	} {
		if err := ValidateID(bad); err == nil {
			t.Errorf("ValidateID(%q) accepted", bad)
		}
	}
	if err := ValidateID("0a1b2c3d-1111-4222-8333-444455556666"); err != nil {
		t.Errorf("valid id rejected: %v", err)
	}
}
