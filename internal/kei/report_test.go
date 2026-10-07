package kei

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeReport(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), reportFile)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var since = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func TestReadReport(t *testing.T) {
	path := writeReport(t, `{"version":"3","timestamp":"2026-10-07T12:00:05.5+00:00","status":"success",
		"options":{"username":"someone@example.com"},"stats":{"assets_seen":238,"downloaded":2,"enumeration_errors":0},
		"failed_assets":[{"id":"A","version_size":"original","error_message":"GET https://p1.icloud-content.com/x/y?token=T failed; see http://h.example/a"}],
		"failed_assets_truncated":4}`)
	r, err := ReadReport(path, since)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "success" || r.Stats.Seen != 238 || r.Stats.Downloaded != 2 || r.FailedOmitted != 4 || len(r.Failed) != 1 {
		t.Fatalf("report = %+v", r)
	}
	if want := "GET https://p1.icloud-content.com/… failed; see http://h.example/…"; r.Failed[0].Error != want {
		t.Fatalf("error = %q, want %q", r.Failed[0].Error, want)
	}
	if !r.Troubled() {
		t.Fatal("a report listing failed assets is not troubled")
	}
}

func TestReadReportRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		path func(t *testing.T) string
		want string
	}{
		"missing":           {func(t *testing.T) string { return filepath.Join(t.TempDir(), reportFile) }, ErrNoReport.Error()},
		"an earlier sync's": {func(t *testing.T) string { return writeReport(t, `{"version":"3","timestamp":"2026-10-07T11:59:59Z"}`) }, ErrNoReport.Error()},
		"another schema":    {func(t *testing.T) string { return writeReport(t, `{"version":"4","timestamp":"2026-10-07T12:00:01Z"}`) }, `schema "4"`},
		"not JSON":          {func(t *testing.T) string { return writeReport(t, `{`) }, "decode kei report"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadReport(tc.path(t), since)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.want == ErrNoReport.Error() && !errors.Is(err, ErrNoReport) {
				t.Fatalf("err = %v, want ErrNoReport", err)
			}
		})
	}
}
