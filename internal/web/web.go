// Package web serves the agent's local HTTP endpoint on a loopback address:
// the Connect API and the web app.
//
// Every request passes, in order:
//
//  1. an exact Host check: the listen address, the browser address
//     (BrowserHost) and the public origin's host when a proxy serves the
//     agent (defeats DNS rebinding),
//  2. an Origin allow-list (the browser address, the public origin and the
//     desktop app's origins),
//  3. CORS, granted only to the desktop app's origins,
//  4. security headers,
//  5. for the API: authentication (bearer token or session cookie, with CSRF
//     protection for cookies), whose principal the request then carries.
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
	"sync"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"

	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3/agentv3connect"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/media"
)

// DesktopOrigins are the origins of the Tauri desktop app's webview:
// http://tauri.localhost on Windows, tauri://localhost on Linux
// (tauri-utils config, useHttpsScheme = false).
var DesktopOrigins = []string{"http://tauri.localhost", "tauri://localhost"}

// BrowserHost is the name a browser on the agent's own computer uses for the
// web app. Browsers resolve every *.localhost name to the loopback address
// themselves (Chrome and Edge, and Firefox since bug 1220810), and keep
// cookies by host name, not port: on its own name the session cookie never
// reaches another service on 127.0.0.1.
const BrowserHost = "homecloud.localhost"

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownWait      = 5 * time.Second
	corsMaxAge        = 2 * time.Hour
	maxRequestBytes   = 1 << 20
	// refusalWindow is how often refused requests (a foreign Host or Origin)
	// are logged: the first at once, the rest as a count.
	refusalWindow = time.Minute
)

// BrowserAddr is the host:port a browser on this computer uses for an agent
// listening on listen.
func BrowserAddr(listen string) (string, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", listen, err)
	}
	return net.JoinHostPort(BrowserHost, port), nil
}

// Options configures Handler.
type Options struct {
	// Listen is the loopback host:port the server is reachable at.
	Listen string
	// PublicOrigin is the https origin a proxy serves the agent at (it
	// forwards the original Host), or empty.
	PublicOrigin string
	// Service implements the API.
	Service agentv3connect.AgentServiceHandler
	// App is the built web app (index.html plus hashed assets/).
	App fs.FS
	// Media serves signed thumbnail and preview URLs (package media).
	Media http.Handler
	Auth  *auth.Authenticator
	Log   *slog.Logger
}

// Handler returns the agent's root HTTP handler.
func Handler(o Options) (http.Handler, error) {
	browser, err := BrowserAddr(o.Listen)
	if err != nil {
		return nil, err
	}
	hosts := []string{o.Listen, browser}
	// The app's own origins; the listen address serves no pages (a browser
	// that opens it is sent to the browser address).
	own := []string{"http://" + browser}
	signIn := []string{browser}
	if o.PublicOrigin != "" {
		u, parseErr := url.Parse(o.PublicOrigin)
		if parseErr != nil {
			return nil, fmt.Errorf("public origin %q: %w", o.PublicOrigin, parseErr)
		}
		hosts = append(hosts, u.Host)
		own = append(own, o.PublicOrigin)
		signIn = append(signIn, u.Host)
	}
	origins := slices.Concat(own, DesktopOrigins)

	csrf := http.NewCrossOriginProtection()
	for _, origin := range own {
		if err := csrf.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("trusted origin %q: %w", origin, err)
		}
	}

	// No request compression: no client sends any, and a small compressed
	// body could make the agent inflate a large one before anyone signed in.
	apiPath, apiHandler := agentv3connect.NewAgentServiceHandler(o.Service,
		connect.WithReadMaxBytes(maxRequestBytes), connect.WithCompression("gzip", nil, nil))
	mux := http.NewServeMux()
	mux.Handle(apiPath, http.MaxBytesHandler(authenticate(o.Auth, csrf, signIn, browser, o.Log, apiHandler), maxRequestBytes))
	app := serveApp(o.App)
	mux.Handle("GET /{$}", toBrowserAddr(o.Listen, browser, app))
	mux.Handle("GET /assets/", app)
	mux.Handle("GET "+media.Prefix, o.Media)

	cors := newCORSPolicy(
		DesktopOrigins,
		connectcors.AllowedMethods(),
		append(connectcors.AllowedHeaders(), "Authorization"),
		connectcors.ExposedHeaders(),
		corsMaxAge,
	)
	refused := &refusals{log: o.Log}
	return checkHost(hosts, refused, checkOrigin(origins, refused, cors.handler(secureHeaders(mux)))), nil
}

// toBrowserAddr sends a browser that opens the app at the listen address to
// the browser address, keeping the URL's fragment (where the app keeps its
// route and a sign-in code), which browsers carry over a redirect.
func toBrowserAddr(listen, browser string, app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == listen {
			http.Redirect(w, r, "http://"+browser+"/", http.StatusTemporaryRedirect)
			return
		}
		app.ServeHTTP(w, r)
	})
}

// refusals logs requests refused for their Host or Origin: the first of each
// window at once, then how many more there were, so that a flood of them
// cannot push the rest of the log out.
type refusals struct {
	log *slog.Logger

	mu    sync.Mutex
	since time.Time
	count int
}

func (f *refusals) refuse(r *http.Request, why string) {
	now := time.Now()
	f.mu.Lock()
	first := now.Sub(f.since) >= refusalWindow
	more := f.count
	if first {
		f.since, f.count = now, 0
	} else {
		f.count++
	}
	f.mu.Unlock()
	if !first {
		return
	}
	if more > 0 {
		f.log.Warn("more requests were refused", "count", more, "window", refusalWindow.String())
	}
	f.log.Warn("request refused", "reason", why, "host", r.Host, "origin", r.Header.Get("Origin"),
		"method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
}

func checkHost(allowed []string, refused *refusals, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(allowed, r.Host) {
			refused.refuse(r, "foreign host")
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func checkOrigin(allowed []string, refused *refusals, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !slices.Contains(allowed, o) {
			refused.refuse(r, "foreign origin")
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

// authenticate admits API calls with a valid credential and passes their
// principal on (auth.PrincipalFrom). ExchangeLoginCode and PairDevice are
// open (they are how a browser or a device obtains a credential; the code is
// the credential); a session is issued only on the browser address or the
// public origin; CreateLoginCode requires the agent token; cookie-
// authenticated calls must also pass CSRF checks.
func authenticate(a *auth.Authenticator, csrf *http.CrossOriginProtection, signIn []string, browser string,
	log *slog.Logger, next http.Handler,
) http.Handler {
	errs := connect.NewErrorWriter()
	deny := func(w http.ResponseWriter, r *http.Request, code connect.Code, msg string) {
		if err := errs.Write(w, r, connect.NewError(code, errors.New(msg))); err != nil {
			http.Error(w, msg, http.StatusInternalServerError)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := strings.TrimSuffix(r.URL.Path, "/")
		switch procedure {
		case agentv3connect.AgentServiceExchangeLoginCodeProcedure:
			// It sets a cookie: no cross-site page may log a browser in, and
			// only on the app's own names may a cookie be set.
			if !slices.Contains(signIn, r.Host) {
				deny(w, r, connect.CodePermissionDenied, "sign in at http://"+browser)
				return
			}
			if err := csrf.Check(r); err != nil {
				deny(w, r, connect.CodePermissionDenied, "cross-origin request rejected")
				return
			}
			next.ServeHTTP(w, r)
			return
		case agentv3connect.AgentServicePairDeviceProcedure:
			// It returns a token to the caller and sets nothing ambient; the
			// desktop app calls it from its own origin.
			next.ServeHTTP(w, r)
			return
		}
		p, err := a.Authenticate(r)
		if err != nil {
			log.Error("credentials could not be checked", "procedure", procedure, "err", err)
			deny(w, r, connect.CodeUnavailable, "credentials cannot be checked right now")
			return
		}
		switch p.Kind {
		case auth.KindBearer:
		case auth.KindDevice:
			if procedure == agentv3connect.AgentServiceCreateLoginCodeProcedure {
				deny(w, r, connect.CodePermissionDenied, "login codes require the agent token")
				return
			}
		case auth.KindSession:
			if procedure == agentv3connect.AgentServiceCreateLoginCodeProcedure {
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
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
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
