package iphonebackup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"howett.net/plist"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const udid = "00008110-001A2B3C4D5E801E"

type fixture struct {
	folder     string // defaults to the UDID
	encrypted  *bool
	state      string
	completed  time.Time
	snapshot   bool // a backup in progress
	noManifest bool
	noStatus   bool
	files      map[string]string
}

func ptr[T any](v T) *T { return &v }

// writeBackup lays out a backup folder like Apple's tools do: Info.plist as
// XML, Manifest.plist and Status.plist as binary plists, hashed files in
// two-hex-digit subfolders.
func writeBackup(t *testing.T, dir string, f fixture) string {
	t.Helper()
	folder := f.folder
	if folder == "" {
		folder = udid
	}
	root := filepath.Join(dir, folder)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(name string, v any, format int) {
		t.Helper()
		data, err := plist.Marshal(v, format)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("Info.plist", map[string]any{
		"Device Name": "Test iPhone", "Product Version": "26.1", "Unique Identifier": udid,
		"Last Backup Date": f.completed,
	}, plist.XMLFormat)
	if !f.noManifest {
		manifest := map[string]any{"Version": "10.0"}
		if f.encrypted != nil {
			manifest["IsEncrypted"] = *f.encrypted
		}
		put("Manifest.plist", manifest, plist.BinaryFormat)
	}
	if !f.noStatus {
		put("Status.plist", map[string]any{
			"SnapshotState": f.state, "Date": f.completed, "IsFullBackup": false, "BackupState": "new", "Version": "3.3",
		}, plist.BinaryFormat)
	}
	if f.snapshot {
		if err := os.MkdirAll(filepath.Join(root, snapshotDir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range f.files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanAndEvaluate(t *testing.T) {
	policy := Policy{MaxAge: 7 * 24 * time.Hour, MaxShrinkPercent: 50}
	tests := []struct {
		name        string
		f           fixture
		lastHealthy int64
		want        State
	}{
		{"healthy", fixture{encrypted: ptr(true), state: "finished", completed: t0.Add(-24 * time.Hour)}, 0, StateOK},
		{"unencrypted", fixture{encrypted: ptr(false), state: "finished", completed: t0.Add(-time.Hour)}, 0, StateUnencrypted},
		{"stale", fixture{encrypted: ptr(true), state: "finished", completed: t0.Add(-10 * 24 * time.Hour)}, 0, StateStale},
		{"did not finish", fixture{encrypted: ptr(true), state: "uploading", completed: t0.Add(-time.Hour)}, 0, StateIncomplete},
		{"first backup in progress", fixture{snapshot: true, noManifest: true, noStatus: true}, 0, StateInProgress},
		{"shrunk", fixture{encrypted: ptr(true), state: "finished", completed: t0.Add(-time.Hour)}, 1 << 30, StateShrunk},
		{"manifest without IsEncrypted", fixture{state: "finished", completed: t0}, 0, StateInvalid},
		{"missing status when idle", fixture{encrypted: ptr(true), noStatus: true}, 0, StateInvalid},
		{"folder not named by UDID", fixture{folder: "something-else", encrypted: ptr(true), state: "finished", completed: t0}, 0, StateInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.f.files = map[string]string{"ab/ab12": "photo", "cd/cd34": "message"}
			writeBackup(t, dir, tt.f)
			current, archived, err := Scan(context.Background(), dir)
			if err != nil || len(current) != 1 || len(archived) != 0 {
				t.Fatalf("Scan = %d current, %d archived, %v", len(current), len(archived), err)
			}
			v := Evaluate(current[0], tt.lastHealthy, policy, t0)
			if v.State != tt.want || (v.State != StateOK && len(v.Reasons) == 0) {
				t.Fatalf("verdict = %+v, want %s with reasons", v, tt.want)
			}
		})
	}
}

func TestScanCountsFilesAndReadsDeviceFacts(t *testing.T) {
	dir := t.TempDir()
	writeBackup(t, dir, fixture{
		encrypted: ptr(true), state: "finished", completed: t0,
		files: map[string]string{"ab/ab12": "12345", "cd/cd34": "678"},
	})
	current, _, err := Scan(context.Background(), dir)
	if err != nil || len(current) != 1 {
		t.Fatalf("Scan = %+v, %v", current, err)
	}
	b := current[0]
	// The three plists plus two backed-up files.
	if b.UDID != udid || b.DeviceName != "Test iPhone" || b.ProductVersion != "26.1" || b.Files != 5 || !b.CompletedAt.Equal(t0) {
		t.Fatalf("backup = %+v", b)
	}
}

func TestScanSeparatesArchivedBackups(t *testing.T) {
	dir := t.TempDir()
	writeBackup(t, dir, fixture{encrypted: ptr(true), state: "finished", completed: t0})
	writeBackup(t, dir, fixture{folder: udid + "-20260901-101500", encrypted: ptr(true), state: "finished", completed: t0.AddDate(0, -1, 0)})
	current, archived, err := Scan(context.Background(), dir)
	if err != nil || len(current) != 1 || len(archived) != 1 || !strings.HasSuffix(archived[0].Dir, "-20260901-101500") {
		t.Fatalf("Scan = %+v, %+v, %v", current, archived, err)
	}
}

func TestScanMissingDirectoryMeansNoBackups(t *testing.T) {
	current, archived, err := Scan(context.Background(), filepath.Join(t.TempDir(), "MobileSync", "Backup"))
	if err != nil || current != nil || archived != nil {
		t.Fatalf("Scan of a missing dir = %v, %v, %v", current, archived, err)
	}
}

func TestScanReportsCorruptPlist(t *testing.T) {
	dir := t.TempDir()
	root := writeBackup(t, dir, fixture{encrypted: ptr(true), state: "finished", completed: t0})
	if err := os.WriteFile(filepath.Join(root, "Status.plist"), []byte("bplist00\x00garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, _, err := Scan(context.Background(), dir)
	if err != nil || len(current) != 1 || current[0].Err == nil {
		t.Fatalf("corrupt Status.plist: %+v, %v", current, err)
	}
	if v := Evaluate(current[0], 0, Policy{MaxAge: time.Hour}, t0); v.State != StateInvalid {
		t.Fatalf("verdict = %+v, want invalid", v)
	}
}

func TestEvaluateOrdersReasonsBySeverity(t *testing.T) {
	b := Backup{UDID: udid, Encrypted: false, SnapshotState: "finished", CompletedAt: t0.Add(-30 * 24 * time.Hour), Unreadable: []string{"x"}}
	v := Evaluate(b, 0, Policy{MaxAge: 7 * 24 * time.Hour, MaxShrinkPercent: 50}, t0)
	if v.State != StateUnreadable || len(v.Reasons) != 3 || !slices.ContainsFunc(v.Reasons, func(r string) bool { return strings.Contains(r, "not encrypted") }) {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Hour:          "1 hour",
		26 * time.Hour:     "26 hours",
		7 * 24 * time.Hour: "7 days",
		240 * time.Hour:    "10 days",
		0:                  "0 hours",
	} {
		if got := Span(d); got != want {
			t.Errorf("Span(%s) = %q, want %q", d, got, want)
		}
	}
}
