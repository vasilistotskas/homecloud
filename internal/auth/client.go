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
			// interceptors run; without one the token could not be sent.
			info, ok := connect.CallInfoForClientContext(ctx)
			if !ok {
				return nil, connect.NewError(connect.CodeInternal, "no call information to carry the bearer token")
			}
			info.RequestHeader().Set("Authorization", header)
			return next(ctx, spec)
		}
	}
}
