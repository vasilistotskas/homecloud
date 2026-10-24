package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
)

func TestFaultsLogsTheAgentsOwnFailures(t *testing.T) {
	cases := map[string]struct {
		err     error
		logged  bool
		mention string
	}{
		"internal, with the cause only the server keeps": {
			err:     connect.NewError(connect.CodeInternal, "store failed").WithCause(errors.New("disk I/O error")),
			logged:  true,
			mention: "disk I/O error",
		},
		"a plain error, which reaches the app as unknown": {
			err:     errors.New("ledger is closed"),
			logged:  true,
			mention: "ledger is closed",
		},
		"a refusal is the caller's": {
			err: connect.NewError(connect.CodeInvalidArgument, "malformed page_token"),
		},
		"no error": {},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			call := faults(slog.New(slog.NewJSONHandler(&buf, nil)))(
				func(context.Context, connect.Spec, connect.ServerStream) error { return c.err })
			if err := call(t.Context(), connect.Spec{Procedure: "/p"}, nil); !errors.Is(err, c.err) {
				t.Fatalf("returned %v, want the handler's own %v", err, c.err)
			}
			line := buf.String()
			if logged := line != ""; logged != c.logged {
				t.Fatalf("logged = %v, want %v: %s", logged, c.logged, line)
			}
			if !strings.Contains(line, c.mention) {
				t.Fatalf("log %q does not mention %q", line, c.mention)
			}
		})
	}
}

func TestFaultsIgnoresCallsWhoseCallerWentAway(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	call := faults(slog.New(slog.NewJSONHandler(&buf, nil)))(
		func(context.Context, connect.Spec, connect.ServerStream) error {
			return connect.NewError(connect.CodeInternal, "send overview")
		})
	_ = call(ctx, connect.Spec{Procedure: "/p"}, nil)
	if buf.Len() != 0 {
		t.Fatalf("logged a call whose caller went away: %s", buf.String())
	}
}
