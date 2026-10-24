package web

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect/v2"

	"github.com/vasilistotskas/homecloud/internal/auth"
)

// faults logs the API answers that are the agent's own failures: the app
// shows them, but only the log keeps them, with the cause the app never
// receives. Refusals (a wrong code, no session, a bad argument) are the
// caller's and are not logged here, nor are calls whose caller went away.
func faults(log *slog.Logger) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			err := next(ctx, spec, stream)
			if err == nil || ctx.Err() != nil {
				return err
			}
			switch code := connect.CodeOf(err); code {
			case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss, connect.CodeUnavailable:
				// The cause a *connect.Error carries for the server alone
				// (connect.Error.WithCause) holds the whole chain.
				logged := err
				if cerr, ok := errors.AsType[*connect.Error](err); ok && cerr.Unwrap() != nil {
					logged = cerr.Unwrap()
				}
				p, _ := auth.PrincipalFrom(ctx)
				log.ErrorContext(ctx, "request failed", "procedure", spec.Procedure, "code", code.String(),
					"principal", p, "err", logged)
			default:
			}
			return err
		}
	}
}
