package iphonebackup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup/iphonebackuptest"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const udid = "00008110-001A2B3C4D5E801E"

var policy = Policy{MaxAge: 7 * 24 * time.Hour, StalledAfter: 6 * time.Hour, MaxShrinkPercent: 50}

type fixture struct {
	folder     string // defaults to the UDID
	encrypted  *bool
	state      string
	completed  time.Time
	snapshot   bool // a backup in progress
	noInfo     bool
	noManifest bool
	noStatus   bool
	files      map[string]string
}

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
	put := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !f.noInfo {
		put("Info.plist", iphonebackuptest.XML(t, map[string]any{"Device Name": "Test iPhone", "Product Version": "26.1", "Unique Identifier": udid}))
	}
	if !f.noManifest {
		manifest := map[string]any{"Version": "10.0"}
		if f.encrypted != nil {
			manifest["IsEncrypted"] = *f.encrypted
		}
		put("Manifest.plist", iphonebackuptest.Binary(t, manifest))
	}
	if !f.noStatus {
		put("Status.plist", iphonebackuptest.Binary(t, map[string]any{
			"SnapshotState": f.state, "Date": f.completed, "IsFullBackup": false, "BackupState": "new", "Version": "3.3",
		}))
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

func scanOne(t *testing.T, f fixture) Backup {
	t.Helper()
	dir := t.TempDir()
	writeBackup(t, dir, f)
	current, archived, err := Scan(t.Context(), dir)
	if err != nil || len(current) != 1 || archived != 0 {
		t.Fatalf("Scan = %d current, %d archived, %v", len(current), archived, err)
	}
	return current[0]
}

func TestJudge(t *testing.T) {
	finished := func(completed time.Time) fixture {
		return fixture{encrypted: new(true), state: "finished", completed: completed}
	}
	tests := []struct {
		name string
		f    fixture
		now  time.Time
		want State
	}{
		{"healthy", finished(t0.Add(-24 * time.Hour)), t0, StateOK},
		{"unencrypted", fixture{encrypted: new(false), state: "finished", completed: t0.Add(-time.Hour)}, t0, StateUnencrypted},
		{"stale", finished(t0.Add(-10 * 24 * time.Hour)), t0, StateStale},
		{"did not finish", fixture{encrypted: new(true), state: "uploading", completed: t0.Add(-time.Hour)}, t0, StateIncomplete},
		{"future date", finished(t0.Add(48 * time.Hour)), t0, StateInvalid},
		{"manifest without IsEncrypted", fixture{state: "finished", completed: t0}, t0, StateInvalid},
		{"missing status when idle", fixture{encrypted: new(true), noStatus: true}, t0, StateInvalid},
		{"folder not named by UDID", fixture{folder: "something-else", encrypted: new(true), state: "finished", completed: t0}, t0, StateInvalid},
		// A first backup writes its plists as it goes: none of that is a problem.
		{"first backup in progress", fixture{snapshot: true, noInfo: true, noManifest: true, noStatus: true}, time.Now(), StateInProgress},
		// A stale device whose new backup runs is not stale meanwhile.
		{"stale while backing up", fixture{snapshot: true, encrypted: new(true), state: "finished", completed: t0.Add(-30 * 24 * time.Hour)}, time.Now(), StateInProgress},
		// A backup that stopped writing long ago never finished.
		{"stalled backup", fixture{snapshot: true, noManifest: true, noStatus: true}, time.Now().Add(7 * time.Hour), StateIncomplete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.f.files = map[string]string{"ab/ab12": "photo", "cd/cd34": "message"}
			v, _ := Judge(scanOne(t, tt.f), 0, Record{}, policy, tt.now)
			if v.State != tt.want || (v.State != StateOK && len(v.Reasons) == 0) {
				t.Fatalf("verdict = %+v, want %s with reasons", v, tt.want)
			}
		})
	}
}

func TestJudgeConfirmsAShrinkBeforeAcceptingIt(t *testing.T) {
	backup := func(bytes int64, completed time.Time) Backup {
		return Backup{UDID: udid, Encrypted: true, SnapshotState: "finished", CompletedAt: completed, Bytes: bytes}
	}
	var rec Record
	steps := []struct {
		b    Backup
		want State
		base int64
	}{
		{backup(1000, t0), StateOK, 1000},
		{backup(400, t0.Add(time.Hour)), StateShrunk, 1000},  // much smaller: suspicious
		{backup(400, t0.Add(time.Hour)), StateShrunk, 1000},  // the same backup again: still suspicious
		{backup(420, t0.Add(2*time.Hour)), StateOK, 420},     // a later one of that size: the new normal
		{backup(900, t0.Add(3*time.Hour)), StateOK, 900},     // growing is normal
		{backup(100, t0.Add(4*time.Hour)), StateShrunk, 900}, // a new suspicious drop
		{backup(890, t0.Add(5*time.Hour)), StateOK, 890},     // back to normal: the drop is forgotten
	}
	for i, s := range steps {
		var v Verdict
		v, rec = Judge(s.b, 0, rec, policy, t0.Add(6*time.Hour))
		if v.State != s.want || rec.Baseline != s.base {
			t.Fatalf("step %d: verdict %s (%v), baseline %d; want %s, %d", i, v.State, v.Reasons, rec.Baseline, s.want, s.base)
		}
	}
}

func TestJudgeCountsUnreadableFiles(t *testing.T) {
	b := Backup{UDID: udid, Encrypted: true, SnapshotState: "finished", CompletedAt: t0, Bytes: 1}
	if v, _ := Judge(b, 1, Record{}, policy, t0); v.State != StateUnreadable {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestScanCountsArchivedCopiesWithoutReadingThem(t *testing.T) {
	dir := t.TempDir()
	writeBackup(t, dir, fixture{encrypted: new(true), state: "finished", completed: t0, files: map[string]string{"ab/ab12": "12345"}})
	// An archived copy with a broken plist is counted, never judged.
	archived := writeBackup(t, dir, fixture{folder: udid + "-20260901-101500", encrypted: new(true), state: "finished", completed: t0})
	if err := os.WriteFile(filepath.Join(archived, "Info.plist"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, n, err := Scan(t.Context(), dir)
	if err != nil || len(current) != 1 || n != 1 {
		t.Fatalf("Scan = %+v, %d archived, %v", current, n, err)
	}
	b := current[0]
	// The three plists plus one backed-up file.
	if b.UDID != udid || b.DeviceName != "Test iPhone" || b.ProductVersion != "26.1" || b.Files != 4 || !b.CompletedAt.Equal(t0) {
		t.Fatalf("backup = %+v", b)
	}
}

func TestScanMissingDirectoryMeansNoBackups(t *testing.T) {
	current, archived, err := Scan(t.Context(), filepath.Join(t.TempDir(), "MobileSync", "Backup"))
	if err != nil || current != nil || archived != 0 {
		t.Fatalf("Scan of a missing dir = %v, %v, %v", current, archived, err)
	}
}

func TestScanReportsCorruptPlist(t *testing.T) {
	dir := t.TempDir()
	root := writeBackup(t, dir, fixture{encrypted: new(true), state: "finished", completed: t0})
	if err := os.WriteFile(filepath.Join(root, "Status.plist"), []byte("bplist00\x00garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, _, err := Scan(t.Context(), dir)
	if err != nil || len(current) != 1 || current[0].Err == nil {
		t.Fatalf("corrupt Status.plist: %+v, %v", current, err)
	}
}

func TestReadAll(t *testing.T) {
	dir := writeBackup(t, t.TempDir(), fixture{files: map[string]string{"ab/ab12": strings.Repeat("x", 3*readBuffer)}})
	if unreadable, err := ReadAll(t.Context(), dir); err != nil || len(unreadable) != 0 {
		t.Fatalf("ReadAll = %v, %v", unreadable, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadAll(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ReadAll: %v", err)
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
