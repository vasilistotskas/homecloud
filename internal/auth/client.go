package auth

import (
	"context"

	"connectrpc.com/connect"
)

// Bearer is a client interceptor that sends token on every unary and
// streaming call.
type Bearer string

var _ connect.Interceptor = Bearer("")

func (b Bearer) header() string { return "Bearer " + string(b) }

// WrapUnary implements connect.Interceptor.
func (b Bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			req.Header().Set("Authorization", b.header())
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient implements connect.Interceptor.
func (b Bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", b.header())
		return conn
	}
}

// WrapStreamingHandler implements connect.Interceptor; servers are unaffected.
func (Bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
