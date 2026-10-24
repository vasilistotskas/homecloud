package auth

import (
	"context"

	"connectrpc.com/connect/v2"
)

// Bearer returns a client interceptor that sends token on every call, unary
// and streaming alike.
func Bearer(token string) connect.ClientInterceptor {
	header := "Bearer " + token
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			// connect.Client attaches a CallInfo to every call before its
			// interceptors run.
			if info, ok := connect.CallInfoForClientContext(ctx); ok {
				info.RequestHeader().Set("Authorization", header)
			}
			return next(ctx, spec)
		}
	}
}
