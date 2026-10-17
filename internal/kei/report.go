package kei

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
)

// reportVersion is the schema of kei's sync report that this package reads
// (kei src/report.rs, v0.25.3: "Schema v3").
const reportVersion = "3"

// Report is what kei recorded about one sync in its JSON report: only its
// outcome and counts. The report's options, which name the Apple account,
// are never read.
type Report struct {
	// Status is "success", "interrupted", "partial_failure" or "session_expired".
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Stats     Stats     `json:"stats"`
	// Failed lists the assets that failed (kei keeps up to 200), and
	// FailedOmitted how many more there were.
	Failed        []FailedAsset `json:"failed_assets"`
	FailedOmitted int           `json:"failed_assets_truncated"`
}

// Stats are the counts of kei's report that say what a sync did, or what
// went wrong while it listed the library.
type Stats struct {
	Seen                  int     `json:"assets_seen"`
	Downloaded            int     `json:"downloaded"`
	Failed                int     `json:"failed"`
	BytesDownloaded       int64   `json:"bytes_downloaded"`
	ElapsedSeconds        float64 `json:"elapsed_secs"`
	EnumerationErrors     int     `json:"enumeration_errors"`
	EnumerationIncomplete bool    `json:"enumeration_incomplete"`
	RateLimited           int     `json:"rate_limited"`
	InventoryDropAssets   int     `json:"inventory_drop_assets"`
	StateWriteFailures    int     `json:"state_write_failures"`
	Interrupted           bool    `json:"interrupted"`
	// SyncTokenBlocked: kei kept its previous iCloud checkpoint (sync token)
	// for safety although no download failed, so its next sync lists the
	// library from that older point again. The other SyncTokenBlocked fields
	// say why, in kei's words: a reason from a vocabulary kei extends, who
	// caused it ("kei", "icloud" or "unknown"), a sentence, and the zone.
	SyncTokenBlocked            bool   `json:"sync_token_blocked"`
	SyncTokenBlockedReason      string `json:"sync_token_blocked_reason"`
	SyncTokenBlockedSource      string `json:"sync_token_blocked_source"`
	SyncTokenBlockedExplanation string `json:"sync_token_blocked_explanation"`
	SyncTokenBlockedZone        string `json:"sync_token_blocked_zone"`
	// FullEnumerationReason is set when this sync listed the whole library
	// instead of the changes since the checkpoint, and says why.
	FullEnumerationReason string `json:"full_enumeration_reason"`
}

// FailedAsset is one asset kei could not download.
type FailedAsset struct {
	ID      string `json:"id"`
	Version string `json:"version_size"`
	Error   string `json:"error_message"`
}

// Troubled reports whether the sync left something to look at: assets
// failed (now, or in an earlier sync and still do: kei lists them from its
// state) or the library could not be listed completely.
func (r Report) Troubled() bool {
	s := r.Stats
	return r.Status != "success" || s.Failed > 0 || len(r.Failed) > 0 || r.FailedOmitted > 0 ||
		s.EnumerationErrors > 0 || s.EnumerationIncomplete || s.InventoryDropAssets > 0 || s.StateWriteFailures > 0
}

// ReportPath is where kei writes its sync report: next to its configuration.
func ReportPath(cfg config.Kei) string {
	return filepath.Join(filepath.Dir(cfg.Config), reportFile)
}

// ErrNoReport is returned when kei wrote no report for the sync, which ended
// before it could.
var ErrNoReport = errors.New("kei wrote no report for this sync")

// ReadReport reads the report kei wrote at path for a sync that started at
// since. A report from an earlier sync is ErrNoReport.
func ReadReport(path string, since time.Time) (Report, error) {
	b, err := os.ReadFile(path) //nolint:gosec // kei's report next to the kei configuration homecloud writes
	if errors.Is(err, fs.ErrNotExist) {
		return Report{}, ErrNoReport
	}
	if err != nil {
		return Report{}, fmt.Errorf("read kei report: %w", err)
	}
	var head struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return Report{}, fmt.Errorf("decode kei report: %w", err)
	}
	if head.Version != reportVersion {
		return Report{}, fmt.Errorf("kei report schema %q, want %q", head.Version, reportVersion)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return Report{}, fmt.Errorf("decode kei report: %w", err)
	}
	if r.Timestamp.Before(since) {
		return Report{}, ErrNoReport
	}
	for i := range r.Failed {
		r.Failed[i].Error = withoutURLs(r.Failed[i].Error)
	}
	return r, nil
}

// urlPattern matches a URL in kei's error messages. Apple's download URLs
// are signed: anyone holding one can fetch the photo until it expires.
var urlPattern = regexp.MustCompile(`(https?://[^\s/?#"']+)[^\s"']*`)

// withoutURLs keeps only the scheme and host of every URL in s.
func withoutURLs(s string) string { return urlPattern.ReplaceAllString(s, "$1/…") }
