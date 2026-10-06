// Package web serves the agent's local HTTP endpoint on a loopback address:
// the Connect API and the web app.
//
// Every request passes, in order:
//
//  1. an exact Host check: the listen address, and the public origin's host
//     when a proxy serves the agent (defeats DNS rebinding),
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
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"

	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v2/agentv2connect"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/media"
)

// DesktopOrigins are the origins of the Tauri desktop app's webview:
// http://tauri.localhost on Windows, tauri://localhost on Linux
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
	// PublicOrigin is the https origin a proxy serves the agent at (it
	// forwards the original Host), or empty.
	PublicOrigin string
	// Service implements the API.
	Service agentv2connect.AgentServiceHandler
	// App is the built web app (index.html plus hashed assets/).
	App fs.FS
	// Media serves signed thumbnail and preview URLs (package media).
	Media http.Handler
	Auth  *auth.Authenticator
	Log   *slog.Logger
}

// Handler returns the agent's root HTTP handler.
func Handler(o Options) (http.Handler, error) {
	hosts := []string{o.Listen}
	own := []string{"http://" + o.Listen}
	if o.PublicOrigin != "" {
		u, err := url.Parse(o.PublicOrigin)
		if err != nil {
			return nil, fmt.Errorf("public origin %q: %w", o.PublicOrigin, err)
		}
		hosts = append(hosts, u.Host)
		own = append(own, o.PublicOrigin)
	}
	origins := slices.Concat(own, DesktopOrigins)

	csrf := http.NewCrossOriginProtection()
	for _, origin := range own {
		if err := csrf.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("trusted origin %q: %w", origin, err)
		}
	}

	apiPath, apiHandler := agentv2connect.NewAgentServiceHandler(o.Service, connect.WithReadMaxBytes(maxRequestBytes))
	mux := http.NewServeMux()
	mux.Handle(apiPath, authenticate(o.Auth, csrf, apiHandler))
	app := serveApp(o.App)
	mux.Handle("GET /{$}", app)
	mux.Handle("GET /assets/", app)
	mux.Handle("GET "+media.Prefix, o.Media)

	cors := newCORSPolicy(
		DesktopOrigins,
		connectcors.AllowedMethods(),
		append(connectcors.AllowedHeaders(), "Authorization"),
		connectcors.ExposedHeaders(),
		corsMaxAge,
	)
	return checkHost(hosts, checkOrigin(origins, cors.handler(secureHeaders(mux)))), nil
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

// authenticate admits API calls with a valid credential. ExchangeLoginCode
// and PairDevice are open (they are how a browser or a device obtains a
// credential; the login code is the credential); CreateLoginCode requires the
// agent token; cookie-authenticated calls must also pass CSRF checks.
func authenticate(a *auth.Authenticator, csrf *http.CrossOriginProtection, next http.Handler) http.Handler {
	errs := connect.NewErrorWriter()
	deny := func(w http.ResponseWriter, r *http.Request, code connect.Code, msg string) {
		if err := errs.Write(w, r, connect.NewError(code, errors.New(msg))); err != nil {
			http.Error(w, msg, http.StatusInternalServerError)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := strings.TrimSuffix(r.URL.Path, "/")
		switch procedure {
		case agentv2connect.AgentServiceExchangeLoginCodeProcedure:
			// It sets a cookie: no cross-site page may log a browser in.
			if err := csrf.Check(r); err != nil {
				deny(w, r, connect.CodePermissionDenied, "cross-origin request rejected")
				return
			}
			next.ServeHTTP(w, r)
			return
		case agentv2connect.AgentServicePairDeviceProcedure:
			// It returns a token to the caller and sets nothing ambient; the
			// desktop app calls it from its own origin.
			next.ServeHTTP(w, r)
			return
		}
		kind, err := a.Authenticate(r)
		if err != nil {
			deny(w, r, connect.CodeUnavailable, "credentials cannot be checked right now")
			return
		}
		switch kind {
		case auth.KindBearer:
		case auth.KindDevice:
			if procedure == agentv2connect.AgentServiceCreateLoginCodeProcedure {
				deny(w, r, connect.CodePermissionDenied, "login codes require the agent token")
				return
			}
		case auth.KindSession:
			if procedure == agentv2connect.AgentServiceCreateLoginCodeProcedure {
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

// assetCache lets browsers keep content-hashed build assets; index.html keeps
// the default no-store so a new agent version is picked up immediately.
const assetCache = "public, max-age=31536000, immutable"

// serveApp serves the web app: index.html at / and the Vite build's hashed
// assets. The app uses hash routing, so no other paths exist. It needs no
// authentication: it holds no data, and every API call it makes is
// authenticated.
func serveApp(app fs.FS) http.Handler {
	files := http.FileServerFS(app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", assetCache)
		}
		files.ServeHTTP(w, r)
	})
}
