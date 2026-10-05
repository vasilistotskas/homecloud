// Package web serves the agent's local HTTP endpoint: the Connect API and
// (from the SPA milestone) the web app, on a loopback address.
//
// Every request passes, in order:
//
//  1. an exact Host allow-list (defeats DNS rebinding),
//  2. an Origin allow-list (the agent's own origins and the desktop app's),
//  3. CORS, granted only to the desktop app's origins,
//  4. security headers,
//  5. for the API: authentication (bearer token or session cookie, with CSRF
//     protection for cookies).
package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"
	"github.com/rs/cors"

	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v1/agentv1connect"
	"github.com/vasilistotskas/homecloud/internal/auth"
)

// DesktopOrigins are the origins of the Tauri desktop app's webview:
// http://tauri.localhost on Windows, tauri://localhost on Linux and macOS
// (tauri-utils config, useHttpsScheme = false).
var DesktopOrigins = []string{"http://tauri.localhost", "tauri://localhost"}

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownWait      = 5 * time.Second
	corsMaxAge        = 2 * time.Hour
	maxRequestBytes   = 1 << 20
)

// Options configures Handler.
type Options struct {
	// Listen is the loopback host:port the server is reachable at.
	Listen string
	// Service implements the API.
	Service agentv1connect.AgentServiceHandler
	Auth    *auth.Authenticator
	Log     *slog.Logger
}

// Handler returns the agent's root HTTP handler.
func Handler(o Options) (http.Handler, error) {
	_, port, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", o.Listen, err)
	}
	hosts := []string{o.Listen, net.JoinHostPort("localhost", port)}
	self := make([]string, 0, len(hosts))
	for _, h := range hosts {
		self = append(self, "http://"+h)
	}
	origins := slices.Concat(self, DesktopOrigins)

	csrf := http.NewCrossOriginProtection()
	for _, o := range self {
		if err := csrf.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("trusted origin %q: %w", o, err)
		}
	}

	apiPath, apiHandler := agentv1connect.NewAgentServiceHandler(o.Service, connect.WithReadMaxBytes(maxRequestBytes))
	mux := http.NewServeMux()
	mux.Handle(apiPath, authenticate(o.Auth, csrf, apiHandler))

	c := cors.New(cors.Options{
		AllowedOrigins:      DesktopOrigins,
		AllowedMethods:      connectcors.AllowedMethods(),
		AllowedHeaders:      append(connectcors.AllowedHeaders(), "Authorization"),
		ExposedHeaders:      connectcors.ExposedHeaders(),
		MaxAge:              int(corsMaxAge / time.Second),
		AllowPrivateNetwork: true,
	})
	return checkHost(hosts, checkOrigin(origins, c.Handler(secureHeaders(mux)))), nil
}

func checkHost(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(allowed, r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func checkOrigin(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !slices.Contains(allowed, o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// authenticate admits API calls with a valid credential. ExchangeLoginCode is
// open (it is how a browser obtains a credential); CreateLoginCode requires the
// bearer token; cookie-authenticated calls must also pass CSRF checks.
func authenticate(a *auth.Authenticator, csrf *http.CrossOriginProtection, next http.Handler) http.Handler {
	errs := connect.NewErrorWriter()
	deny := func(w http.ResponseWriter, r *http.Request, code connect.Code, msg string) {
		if err := errs.Write(w, r, connect.NewError(code, errors.New(msg))); err != nil {
			http.Error(w, msg, http.StatusInternalServerError)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := strings.TrimSuffix(r.URL.Path, "/")
		if procedure == agentv1connect.AgentServiceExchangeLoginCodeProcedure {
			if err := csrf.Check(r); err != nil {
				deny(w, r, connect.CodePermissionDenied, "cross-origin request rejected")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		switch a.Authenticate(r) {
		case auth.KindBearer:
		case auth.KindSession:
			if procedure == agentv1connect.AgentServiceCreateLoginCodeProcedure {
				deny(w, r, connect.CodePermissionDenied, "login codes require the agent token")
				return
			}
			if err := csrf.Check(r); err != nil {
				deny(w, r, connect.CodePermissionDenied, "cross-origin request rejected")
				return
			}
		case auth.KindNone:
			deny(w, r, connect.CodeUnauthenticated, "missing or invalid credentials")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve serves h on ln until ctx ends. Streaming responses (WatchOverview) are
// long-lived, so there is no server-wide write timeout; the listener is
// loopback-only.
func Serve(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}
		if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}
}
