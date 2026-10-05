package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

type fakeLedger struct{}

func (fakeLedger) StateCounts(context.Context) (map[ledger.AssetState]int, error) {
	return map[ledger.AssetState]int{ledger.StateVerified: 1777, ledger.StateQuarantined: 1}, nil
}

func (fakeLedger) RecentRuns(context.Context, int) ([]ledger.Run, error) {
	ok := true
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Second)
	return []ledger.Run{{ID: 1, Kind: "cycle", StartedAt: start, FinishedAt: &end, OK: &ok, Detail: `{"kei_outcome":"ok"}`}}, nil
}

func (fakeLedger) RecentEvents(context.Context, []string, int) ([]ledger.Event, error) {
	return []ledger.Event{{At: time.Now(), Asset: "<script>A1</script>", Kind: "quarantined", Detail: `{"tier":"structure"}`}}, nil
}

const host = "127.0.0.1:8790"

func serve(t *testing.T, method, target, hostHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	req.Host = hostHeader
	rec := httptest.NewRecorder()
	Handler(fakeLedger{}, host, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(rec, req)
	return rec
}

func TestStatusPage(t *testing.T) {
	rec := serve(t, http.MethodGet, "/", host)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{"1777", "quarantined", "1m30s", `class="ok"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(page, "<script>A1</script>") {
		t.Error("asset id rendered without HTML escaping")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	if rec := serve(t, http.MethodGet, "/", "evil.example:8790"); rec.Code != http.StatusForbidden {
		t.Fatalf("DNS-rebinding host: status = %d, want 403", rec.Code)
	}
}

func TestOnlyGETRoot(t *testing.T) {
	if rec := serve(t, http.MethodPost, "/", host); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: status = %d, want 405", rec.Code)
	}
	if rec := serve(t, http.MethodGet, "/other", host); rec.Code != http.StatusNotFound {
		t.Errorf("GET /other: status = %d, want 404", rec.Code)
	}
}
