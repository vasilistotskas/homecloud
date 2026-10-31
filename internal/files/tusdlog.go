package files

import (
	"context"
	"log/slog"

	expslog "golang.org/x/exp/slog"
)

// tusdLog passes tusd's records at or above level to the agent's log. tusd
// v2 logs through golang.org/x/exp/slog, a separate implementation whose
// types log/slog does not accept; its move to log/slog waits for tusd's next
// major release (tus/tusd#1201).
type tusdLog struct {
	h     slog.Handler
	level slog.Level
}

var _ expslog.Handler = tusdLog{}

// Enabled implements expslog.Handler.
func (t tusdLog) Enabled(ctx context.Context, l expslog.Level) bool {
	return slog.Level(l) >= t.level && t.h.Enabled(ctx, slog.Level(l))
}

// Handle implements expslog.Handler.
func (t tusdLog) Handle(ctx context.Context, r expslog.Record) error {
	out := slog.NewRecord(r.Time, slog.Level(r.Level), r.Message, r.PC)
	r.Attrs(func(a expslog.Attr) bool {
		out.AddAttrs(convertAttr(a))
		return true
	})
	return t.h.Handle(ctx, out) //nolint:wrapcheck // the agent's own handler
}

// WithAttrs implements expslog.Handler.
func (t tusdLog) WithAttrs(attrs []expslog.Attr) expslog.Handler {
	converted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		converted[i] = convertAttr(a)
	}
	return tusdLog{h: t.h.WithAttrs(converted), level: t.level}
}

// WithGroup implements expslog.Handler.
func (t tusdLog) WithGroup(name string) expslog.Handler {
	return tusdLog{h: t.h.WithGroup(name), level: t.level}
}

func convertAttr(a expslog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == expslog.KindGroup {
		group := v.Group()
		attrs := make([]any, len(group))
		for i, g := range group {
			attrs[i] = convertAttr(g)
		}
		return slog.Group(a.Key, attrs...)
	}
	return slog.Any(a.Key, v.Any())
}
