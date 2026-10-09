package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/iphonebackup/iphonebackuptest"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

const testUDID = "00008110-001A2B3C4D5E801E"

// writeIPhoneBackup writes a minimal finished backup of testUDID into dir.
func writeIPhoneBackup(t *testing.T, dir string, encrypted bool, completed time.Time) {
	t.Helper()
	root := filepath.Join(dir, testUDID)
	if err := os.MkdirAll(filepath.Join(root, "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"Info.plist":     iphonebackuptest.XML(t, map[string]any{"Device Name": "Test iPhone", "Product Version": "26.1", "Unique Identifier": testUDID}),
		"Manifest.plist": iphonebackuptest.Binary(t, map[string]any{"IsEncrypted": encrypted}),
		"Status.plist":   iphonebackuptest.Binary(t, map[string]any{"SnapshotState": "finished", "Date": completed}),
	} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ab", "ab12"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func countTitle(n *recordingNotifier, title string) int {
	c := 0
	for _, got := range n.titles() {
		if got == title {
			c++
		}
	}
	return c
}

func TestIPhoneBackupCheckWithoutBackups(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	rep, ok := agent.checkIPhoneBackups(t.Context())
	r, isReport := rep.(IPhoneBackupReport)
	if ok || !isReport || len(r.Devices) != 0 || r.Error != "" {
		t.Fatalf("check without backups = %+v, ok=%v; want a completed, unhealthy check", rep, ok)
	}
	if countTitle(n, "homecloud: no iPhone backup yet") != 1 {
		t.Fatalf("alerts = %v", n.titles())
	}
}

func TestIPhoneBackupCheckRecordsStateAndAlertsOnceADay(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	ctx := t.Context()
	dir := agent.cfg.IPhoneBackup.Dirs[0]

	writeIPhoneBackup(t, dir, true, clock.Add(-time.Hour))
	if err := agent.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	run := lastRun(t, agent, IPhoneBackupRunKind)
	rep, err := ParseIPhoneBackupReport(run.Detail)
	if err != nil || !*run.OK || len(rep.Devices) != 1 || rep.Devices[0].State != iphonebackup.StateOK || rep.Devices[0].Name != "Test iPhone" {
		t.Fatalf("healthy check: ok=%v report=%+v, %v", *run.OK, rep, err)
	}

	// The next check finds it unencrypted: one alert, and again only a day later.
	writeIPhoneBackup(t, dir, false, clock)
	for range 3 {
		clock = clock.Add(25 * time.Hour)
		writeIPhoneBackup(t, dir, false, clock)
		if err = agent.maintain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := countTitle(n, "homecloud: iPhone backup needs attention"); got != 3 {
		t.Fatalf("attention alerts over three daily checks = %d, want 3 (one per day)", got)
	}
	events, err := agent.db.Events(ctx, ledger.EventQuery{Kinds: []string{ledger.EventIPhoneBackupChanged}}, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("state change events = %d, %v; want ok then unencrypted", len(events), err)
	}
	// ntfy topics are public: no alert may identify the device.
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, m := range n.msgs {
		for _, secret := range []string{testUDID, "Test iPhone"} {
			if strings.Contains(m.Title+m.Body, secret) {
				t.Fatalf("alert %q names the device (%s)", m.Title, secret)
			}
		}
	}
}

func TestIPhoneBackupFullReadRunsOnItsOwnInterval(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "0", &clock)
	ctx := t.Context()
	writeIPhoneBackup(t, agent.cfg.IPhoneBackup.Dirs[0], true, clock)
	readAt := func() time.Time {
		t.Helper()
		recs, err := agent.db.IPhoneBackupRecords(ctx)
		if err != nil || len(recs) != 1 {
			t.Fatalf("records = %v, %v", recs, err)
		}
		for _, r := range recs {
			if r.LastFullReadAt == nil {
				t.Fatal("never read in full")
			}
			return *r.LastFullReadAt
		}
		return time.Time{}
	}
	if _, ok := agent.checkIPhoneBackups(ctx); !ok {
		t.Fatal("first check not healthy")
	}
	first := readAt()
	clock = clock.Add(24 * time.Hour) // a daily check, inside the weekly read interval
	agent.checkIPhoneBackups(ctx)
	if !readAt().Equal(first) {
		t.Fatal("the full read ran again before read_interval passed")
	}
	clock = clock.Add(7 * 24 * time.Hour)
	agent.checkIPhoneBackups(ctx)
	if !readAt().After(first) {
		t.Fatal("the full read did not run after read_interval")
	}
}

func TestIPhoneBackupUnreadableDirectoryAlerts(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	// A file where the backup directory should be cannot be listed.
	dir := agent.cfg.IPhoneBackup.Dirs[0]
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, ok := agent.checkIPhoneBackups(t.Context())
	if r := rep.(IPhoneBackupReport); ok || len(r.DirErrors) != 1 {
		t.Fatalf("check = %+v, ok=%v", r, ok)
	}
	if countTitle(n, "homecloud: iPhone backups could not be checked") != 1 {
		t.Fatalf("alerts = %v", n.titles())
	}
}

// lastIPhoneReport returns the report of the agent's last iPhone backup check.
func lastIPhoneReport(t *testing.T, agent *Agent) (IPhoneBackupReport, bool) {
	t.Helper()
	run := lastRun(t, agent, IPhoneBackupRunKind)
	rep, err := ParseIPhoneBackupReport(run.Detail)
	if err != nil {
		t.Fatal(err)
	}
	return rep, *run.OK
}

func TestIPhoneBackupCopiesAreKeptAndPruned(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	agent.cfg.IPhoneBackup.Keep = 2
	ctx := t.Context()
	dir := agent.cfg.IPhoneBackup.Dirs[0]
	for i := range 3 {
		writeIPhoneBackup(t, dir, true, clock.Add(-time.Hour))
		if _, ok := agent.checkIPhoneBackups(ctx); !ok {
			t.Fatalf("check %d not healthy: %v", i, n.titles())
		}
		clock = clock.Add(25 * time.Hour)
	}
	copies, err := agent.db.IPhoneBackupCopies(ctx)
	if err != nil || len(copies) != 2 || copies[0].Stamp != iphonecopy.Stamp(clock.Add(-26*time.Hour)) {
		t.Fatalf("copies = %+v, %v", copies, err)
	}
	events, err := agent.db.Events(ctx, ledger.EventQuery{Kinds: []string{ledger.EventIPhoneBackupCopied, ledger.EventIPhoneBackupCopyRemoved}}, 0, 10)
	if err != nil || len(events) != 4 {
		t.Fatalf("copy events = %d, %v; want three copies and one removal", len(events), err)
	}
	copyDir := filepath.Join(agent.cfg.Storage.Root, IPhoneCopiesFolder, testUDID, copies[0].Stamp, "ab", "ab12")
	if b, readErr := os.ReadFile(copyDir); readErr != nil || string(b) != "data" { //nolint:gosec // a test file
		t.Fatalf("copied file = %q, %v", b, readErr)
	}
}

func TestIPhoneBackupCopyDamageIsFoundAndAlerted(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	agent.cfg.IPhoneBackup.Keep = 3
	ctx := t.Context()
	writeIPhoneBackup(t, agent.cfg.IPhoneBackup.Dirs[0], true, clock.Add(-time.Hour))
	if _, ok := agent.checkIPhoneBackups(ctx); !ok {
		t.Fatal("first check not healthy")
	}
	copies, err := agent.db.IPhoneBackupCopies(ctx)
	if err != nil || len(copies) != 1 {
		t.Fatalf("copies = %+v, %v", copies, err)
	}
	damaged := filepath.Join(agent.cfg.Storage.Root, IPhoneCopiesFolder, testUDID, copies[0].Stamp, "ab", "ab12")
	if err = os.WriteFile(damaged, []byte("rot!"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(agent.cfg.IPhoneBackup.ReadInterval.Std())
	writeIPhoneBackup(t, agent.cfg.IPhoneBackup.Dirs[0], true, clock.Add(-time.Hour))
	if _, ok := agent.checkIPhoneBackups(ctx); ok {
		t.Fatal("a damaged copy must make the check unhealthy")
	}
	if countTitle(n, "homecloud: an iPhone backup copy is damaged") != 1 {
		t.Fatalf("alerts = %v", n.titles())
	}
	copies, err = agent.db.IPhoneBackupCopies(ctx)
	if err != nil || len(copies) != 2 || copies[1].DamagedFiles != 1 || copies[0].DamagedFiles != 0 {
		t.Fatalf("copies after damage = %+v, %v", copies, err)
	}
	// The new copy was made whole, not linked to the damaged one.
	if b, err := os.ReadFile(filepath.Join(agent.cfg.Storage.Root, IPhoneCopiesFolder, testUDID, copies[0].Stamp, "ab", "ab12")); err != nil || string(b) != "data" {
		t.Fatalf("new copy = %q, %v", b, err)
	}
}

func TestIPhoneBackupsPushedFromAnotherComputer(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	agent.cfg.IPhoneBackup.Dirs = nil
	agent.cfg.IPhoneBackup.Incoming = t.TempDir()
	agent.cfg.IPhoneBackup.Keep = 3
	ctx := t.Context()

	// Both computers hold the push key.
	key := iphonecopy.PushKey{7}
	keyPath, err := agent.pushKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := iphonecopy.SavePushKey(keyPath, key); err != nil {
		t.Fatal(err)
	}

	// The computer with the backups pushes one.
	pc := t.TempDir()
	writeIPhoneBackup(t, pc, true, clock.Add(-time.Hour))
	b := iphonebackup.Inspect(ctx, filepath.Join(pc, testUDID), testUDID)
	if res, err := iphonecopy.Push(ctx, b, agent.cfg.IPhoneBackup.Incoming, key); err != nil || res.Outcome != iphonecopy.PushSent {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if err := agent.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	rep, ok := lastIPhoneReport(t, agent)
	if !ok || len(rep.Devices) != 1 {
		t.Fatalf("check after a push: ok=%v report=%+v alerts=%v", ok, rep, n.titles())
	}
	d := rep.Devices[0]
	if !d.FromCopy || d.State != iphonebackup.StateOK || d.Name != "Test iPhone" || len(d.Copies) != 1 || !d.Encrypted {
		t.Fatalf("device = %+v", d)
	}
	// Nothing pushed for longer than max_age: the newest copy is stale.
	clock = clock.Add(agent.cfg.IPhoneBackup.MaxAge.Std() + 2*time.Hour)
	if _, ok := agent.checkIPhoneBackups(ctx); ok {
		t.Fatal("a stale copy must make the check unhealthy")
	}
	if countTitle(n, "homecloud: iPhone backup needs attention") != 1 {
		t.Fatalf("alerts = %v", n.titles())
	}
}

// TestPushesWaitForAPushKey: without this computer's push key no push can be
// checked, so pushes stay in incoming and the owner is told how to fix it.
func TestPushesWaitForAPushKey(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	agent.cfg.IPhoneBackup.Dirs = nil
	agent.cfg.IPhoneBackup.Incoming = t.TempDir()
	agent.cfg.IPhoneBackup.Keep = 3
	ctx := t.Context()

	pc := t.TempDir()
	writeIPhoneBackup(t, pc, true, clock.Add(-time.Hour))
	b := iphonebackup.Inspect(ctx, filepath.Join(pc, testUDID), testUDID)
	if _, err := iphonecopy.Push(ctx, b, agent.cfg.IPhoneBackup.Incoming, iphonecopy.PushKey{7}); err != nil {
		t.Fatal(err)
	}
	rep, ok := agent.checkIPhoneBackups(ctx)
	report, isReport := rep.(IPhoneBackupReport)
	if ok || !isReport || len(report.PushErrors) != 1 || !strings.Contains(report.PushErrors[0], "push-key") {
		t.Fatalf("check without a key: ok=%v report=%+v", ok, rep)
	}
	if countTitle(n, "homecloud: a pushed iPhone backup was not taken in") != 1 {
		t.Fatalf("alerts = %v", n.titles())
	}
	if _, err := os.Stat(filepath.Join(agent.cfg.IPhoneBackup.Incoming, testUDID, iphonecopy.Stamp(b.CompletedAt)+".sha256")); err != nil {
		t.Fatalf("the push did not wait in incoming: %v", err)
	}
}
