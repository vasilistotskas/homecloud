package web

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/vasilistotskas/homecloud/internal/auth"
)

// faults logs the API answers that are the agent's own failures: the app
// shows them, but only the log keeps them. Refusals (a wrong code, no
// session, a bad argument) are the caller's and are not logged here, nor
// are calls whose caller went away.
type faults struct{ log *slog.Logger }

var _ connect.Interceptor = faults{}

func (f faults) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		f.check(ctx, req.Spec().Procedure, err)
		return res, err
	}
}

// WrapStreamingClient leaves clients alone: the agent serves, it does not call.
func (faults) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (f faults) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		f.check(ctx, conn.Spec().Procedure, err)
		return err
	}
}

func (f faults) check(ctx context.Context, procedure string, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	switch code := connect.CodeOf(err); code {
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss, connect.CodeUnavailable:
		p, _ := auth.PrincipalFrom(ctx)
		f.log.ErrorContext(ctx, "request failed", "procedure", procedure, "code", code.String(),
			"principal", p, "err", err)
	default:
	}
}
