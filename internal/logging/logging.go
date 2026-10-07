// Package logging carries what a log line belongs to through a context: the
// run (a cycle, a scrub, an iCloud Drive sync…) whose work wrote it. Handler
// adds it to every record logged with that context, so the lines of one run
// can be told apart from the API's and from other runs'.
package logging

import (
	"context"
	"log/slog"
)

// Keys of the attributes Handler adds.
const (
	// KeyRun is the run's id in the ledger (runs.id).
	KeyRun = "run"
	// KeyRunKind is the run's kind (runs.kind).
	KeyRunKind = "run_kind"
)

type runKey struct{}

type run struct {
	id   int64
	kind string
}

// WithRun returns ctx carrying the run with id and kind.
func WithRun(ctx context.Context, id int64, kind string) context.Context {
	return context.WithValue(ctx, runKey{}, run{id: id, kind: kind})
}

// Handler wraps h, adding the run carried by a record's context.
func Handler(h slog.Handler) slog.Handler { return runHandler{h} }

type runHandler struct{ slog.Handler }

func (h runHandler) Handle(ctx context.Context, r slog.Record) error {
	if v, ok := ctx.Value(runKey{}).(run); ok {
		r = r.Clone()
		r.AddAttrs(slog.Int64(KeyRun, v.id), slog.String(KeyRunKind, v.kind))
	}
	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // a handler passes its inner handler's error on unchanged
}

func (h runHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return runHandler{h.Handler.WithAttrs(attrs)}
}

func (h runHandler) WithGroup(name string) slog.Handler {
	return runHandler{h.Handler.WithGroup(name)}
}
