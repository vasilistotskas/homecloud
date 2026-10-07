package logging

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"log/slog"
	"testing"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &m); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return m
}

func TestHandlerAddsTheRun(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(Handler(slog.NewJSONHandler(&buf, nil))).With("component", "test")

	log.InfoContext(WithRun(context.Background(), 42, "cycle"), "ingested", "assets", 3)
	m := decode(t, buf.Bytes())
	if m[KeyRun] != float64(42) || m[KeyRunKind] != "cycle" || m["component"] != "test" || m["assets"] != float64(3) {
		t.Fatalf("line in a run = %v", m)
	}

	buf.Reset()
	log.InfoContext(context.Background(), "local API listening")
	if m := decode(t, buf.Bytes()); m[KeyRun] != nil || m[KeyRunKind] != nil {
		t.Fatalf("line outside a run = %v", m)
	}
}

// TestHandlerKeepsGroups: a run's attributes stay inside a group, like every
// attribute added after it.
func TestHandlerKeepsGroups(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(Handler(slog.NewJSONHandler(&buf, nil))).WithGroup("g")
	log.InfoContext(WithRun(context.Background(), 7, "scrub"), "scrubbed")
	g, ok := decode(t, buf.Bytes())["g"].(map[string]any)
	if !ok || g[KeyRun] != float64(7) {
		t.Fatalf("grouped line = %s", buf.Bytes())
	}
}
