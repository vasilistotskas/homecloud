package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/verify"
)

// TestMismatchNotRecordedIsNoQuarantine: when the ledger cannot record a
// mismatch, nothing was quarantined, so the outcome must not count (or be
// logged and alerted) as one; it is retried as a transient error.
func TestMismatchNotRecordedIsNoQuarantine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	photo := filepath.Join(dir, "IMG_0001.HEIC")
	if err := os.WriteFile(photo, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := ledger.Open(ctx, filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil { // every write now fails
		t.Fatal(err)
	}
	p := ledger.PendingResource{
		Key:          ledger.Key{Library: "PrimarySync", AssetID: "A1", Version: "original"},
		LocalPath:    photo,
		SizeBytes:    uint64(len("content")),
		SHA256:       strings.Repeat("0", 64),
		NeedsContent: true,
	}
	err = verifyOne(ctx, &config.Config{}, db, p, time.Now)
	if err == nil || errors.Is(err, verify.ErrMismatch) {
		t.Fatalf("unrecorded mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "record content mismatch") {
		t.Fatalf("the error does not say the mismatch was not recorded: %v", err)
	}
}
