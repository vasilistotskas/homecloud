package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup/iphonebackuptest"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/platform/platformtest"
)

func TestIPhonePush(t *testing.T) {
	platformtest.StateDir(t)
	if out, err := cli(t, iphonecopy.PushKey{5}.String()+"\n", "iphone", "push-key", "--set"); err != nil {
		t.Fatalf("push-key --set: %v\n%s", err, out)
	}
	backups, incoming := t.TempDir(), t.TempDir()
	iphonebackuptest.WriteBackup(t, backups, iphonebackuptest.Backup{
		UDID: "00008110-001A2B3C4D5E801E", DeviceName: "Test iPhone",
		Completed: time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC), Encrypted: true,
		Files: map[string]string{"Manifest.db": "manifest", "0a/0a1b2c3d": "photos"},
	})
	push := func() string {
		t.Helper()
		var out bytes.Buffer
		err := newApp().execute(t.Context(), &call{stdout: &out, stderr: &out},
			[]string{"iphone", "push", "--from", backups, "--to", incoming})
		if err != nil {
			t.Fatalf("push: %v\n%s", err, out.String())
		}
		return out.String()
	}
	if got := push(); !strings.HasPrefix(got, "Test iPhone: pushed the backup of ") || !strings.Contains(got, ": 5 of 5 files") {
		t.Fatalf("first push printed %q", got)
	}
	if got := push(); !strings.Contains(got, "waits for homecloud to take it in") {
		t.Fatalf("second push printed %q", got)
	}
	if _, err := os.Stat(filepath.Join(incoming, "00008110-001A2B3C4D5E801E", "20261007T030000Z.sha256")); err != nil {
		t.Fatalf("no committed push: %v", err)
	}
}

func TestIPhonePushNeedsAbsoluteFolders(t *testing.T) {
	var out bytes.Buffer
	err := newApp().execute(t.Context(), &call{stdout: &out, stderr: &out},
		[]string{"iphone", "push", "--from", "relative", "--to", t.TempDir()})
	if exitStatus(err) != 2 {
		t.Fatalf("relative --from: err = %v (status %d)", err, exitStatus(err))
	}
}

// cli runs the command line with stdin and returns what it printed.
func cli(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := newApp().execute(t.Context(), &call{stdin: strings.NewReader(stdin), stdout: &out, stderr: &out}, args)
	return out.String(), err
}

func TestIPhonePushKey(t *testing.T) {
	state := platformtest.StateDir(t)
	out, err := cli(t, "", "iphone", "push-key")
	if err != nil {
		t.Fatalf("push-key: %v\n%s", err, out)
	}
	saved, err := iphonecopy.LoadPushKey(filepath.Join(state, iphonecopy.PushKeyFile))
	if err != nil || !strings.Contains(out, saved.String()) {
		t.Fatalf("push-key printed %q; saved %v, %v", out, saved, err)
	}
	if again, err := cli(t, "", "iphone", "push-key"); err != nil || again != out {
		t.Fatalf("a second push-key printed %q, %v; want the same key", again, err)
	}
	if out, err := cli(t, "not a key\n", "iphone", "push-key", "--set"); err == nil {
		t.Fatalf("a malformed key was saved: %s", out)
	}
}

func TestIPhonePushNeedsAKey(t *testing.T) {
	platformtest.StateDir(t)
	out, err := cli(t, "", "iphone", "push", "--from", t.TempDir(), "--to", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "push-key --set") {
		t.Fatalf("push without a key: %v\n%s", err, out)
	}
}
