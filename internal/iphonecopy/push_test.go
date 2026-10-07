package iphonecopy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup/iphonebackuptest"
)

// fileAs files the backup folder src in incoming as device's push of the
// backup finished at stamp, signed with key: what a pushing computer does,
// or a hostile one that holds the key could.
func fileAs(t *testing.T, incoming, src, device, stamp string, key PushKey) {
	t.Helper()
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close() //nolint:errcheck // read-only
	m, err := HashTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(incoming, device, stamp)
	for rel := range m {
		data, err := root.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil { //nolint:gosec // rel passed HashTree's checks: a name Apple's tools write
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(incoming, device, stamp+signatureExt), key.signature(device, stamp, m.Bytes()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+manifestExt, m.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// refused takes in incoming's one push and returns why it was refused.
func refused(t *testing.T, a *Archive, incoming string) string {
	t.Helper()
	got, err := a.Ingest(t.Context(), incoming, testKey, testNow, newest(a))
	if err != nil || len(got) != 1 || got[0].Err == nil {
		t.Fatalf("ingest = %+v, %v; want one refused push", got, err)
	}
	if snaps, _ := a.Snapshots(got[0].UDID); len(snaps) != 0 {
		t.Fatalf("a refused push was archived: %+v", snaps)
	}
	return got[0].Err.Error()
}

func TestIngestTakesOnlySignedPushes(t *testing.T) {
	for name, tt := range map[string]struct {
		key  PushKey
		sign bool
		want string
	}{
		"unsigned":      {sign: false, want: "not signed"},
		"another key":   {key: PushKey{9}, sign: true, want: "not signed with this archive's push key"},
		"the right key": {key: testKey, sign: true},
	} {
		t.Run(name, func(t *testing.T) {
			a := openArchive(t)
			incoming := t.TempDir()
			b := backup(t, t.TempDir(), t1, "v1")
			fileAs(t, incoming, b.Dir, udid, Stamp(t1), tt.key)
			if !tt.sign {
				if err := os.Remove(filepath.Join(incoming, udid, Stamp(t1)+signatureExt)); err != nil {
					t.Fatal(err)
				}
			}
			if tt.want == "" {
				got, err := a.Ingest(t.Context(), incoming, testKey, testNow, newest(a))
				if err != nil || len(got) != 1 || got[0].Err != nil {
					t.Fatalf("ingest = %+v, %v", got, err)
				}
				return
			}
			if why := refused(t, a, incoming); !strings.Contains(why, tt.want) {
				t.Fatalf("refused because %q, want %q", why, tt.want)
			}
		})
	}
}

// TestIngestChecksThePushedBackup: a push signed with the right key must
// still be what it is filed as. The pushing computer, if compromised, could
// otherwise file any backup under any device, or date one in the future to
// make every genuine copy the oldest (pruning keeps the newest).
func TestIngestChecksThePushedBackup(t *testing.T) {
	const other = "00008110-00FFFFFFFFFFFFFE"
	write := func(t *testing.T, device string, completed time.Time, encrypted bool) string {
		t.Helper()
		return iphonebackuptest.WriteBackup(t, t.TempDir(), iphonebackuptest.Backup{
			UDID: device, DeviceName: "Test iPhone", Completed: completed, Encrypted: encrypted, Files: files("v1"),
		})
	}
	future := testNow.Add(48 * time.Hour)
	for name, tt := range map[string]struct {
		src, device, stamp string
		want               string
	}{
		"another device's backup":  {write(t, other, t1, true), udid, Stamp(t1), "of device"},
		"an unencrypted backup":    {write(t, udid, t1, false), udid, Stamp(t1), "not encrypted"},
		"a backup from the future": {write(t, udid, future, true), udid, Stamp(future), "in the future"},
		"filed under another time": {write(t, udid, t1, true), udid, Stamp(t1.Add(time.Hour)), "not at"},
	} {
		t.Run(name, func(t *testing.T) {
			a := openArchive(t)
			incoming := t.TempDir()
			fileAs(t, incoming, tt.src, tt.device, tt.stamp, testKey)
			if why := refused(t, a, incoming); !strings.Contains(why, tt.want) {
				t.Fatalf("refused because %q, want %q", why, tt.want)
			}
		})
	}
}

// TestPushAfterAnInterruptedCommit: a push renamed into place without its
// manifest (the commit stopped between its two renames) was never committed
// and must not block the next push of that backup.
func TestPushAfterAnInterruptedCommit(t *testing.T) {
	incoming := t.TempDir()
	b := backup(t, t.TempDir(), t1, "v1")
	if err := os.MkdirAll(filepath.Join(incoming, udid, Stamp(t1), "0a"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := Push(t.Context(), b, incoming, testKey)
	if err != nil || res.Outcome != PushSent {
		t.Fatalf("push = %+v, %v", res, err)
	}
	a := openArchive(t)
	if got, err := a.Ingest(t.Context(), incoming, testKey, testNow, newest(a)); err != nil || len(got) != 1 || got[0].Err != nil {
		t.Fatalf("ingest = %+v, %v", got, err)
	}
}

func TestPushKeyRoundTrip(t *testing.T) {
	k, err := NewPushKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), PushKeyFile)
	if _, err := LoadPushKey(path); err != ErrNoPushKey { //nolint:errorlint // returned as is
		t.Fatalf("no key yet: %v", err)
	}
	if err := SavePushKey(path, k); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadPushKey(path); err != nil || got != k {
		t.Fatalf("loaded %v, %v", got, err)
	}
	if _, err := ParsePushKey("too short"); err == nil {
		t.Fatal("a malformed key was accepted")
	}
}
