package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// corsPolicy grants cross-origin access to a fixed set of origins, following
// the Fetch standard's CORS protocol
// (https://fetch.spec.whatwg.org/#http-cors-protocol). Credentials are never
// allowed: callers send a bearer token in Authorization, which must therefore
// be listed among the allowed headers (a wildcard would not cover it).
//
// No Access-Control-Allow-Private-Network header is sent. Chrome put Private
// Network Access, which asked for it in preflights, on hold (2024-03-18);
// Local Network Access replaced it (enforced from Chromium 142, 2025-10-28)
// and gates local requests on a user permission, never on a CORS header, and
// exempts requests from loopback (WICG Local Network Access, 2026-08-07). The
// desktop app's WebView2 (Edge 154) sends no
// Access-Control-Request-Private-Network header in its preflights to the
// agent (observed 2026-10-06).
type corsPolicy struct {
	origins []string
	methods []string
	headers []string

	allowMethods  string
	allowHeaders  string
	exposeHeaders string
	maxAge        string
}

func newCORSPolicy(origins, methods, headers, exposed []string, maxAge time.Duration) *corsPolicy {
	return &corsPolicy{
		origins:       origins,
		methods:       methods,
		headers:       headers,
		allowMethods:  strings.Join(methods, ", "),
		allowHeaders:  strings.Join(headers, ", "),
		exposeHeaders: strings.Join(exposed, ", "),
		maxAge:        strconv.Itoa(int(maxAge / time.Second)),
	}
}

// handler answers CORS preflights itself and adds the CORS response headers
// to every other request from an allowed origin. A preflight that is not
// allowed is refused without any CORS header, so it reveals nothing of the
// policy. An OPTIONS request that is not a preflight passes through.
func (p *corsPolicy) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		origin := r.Header.Get("Origin")
		if r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			// Which preflights succeed depends on all three request headers.
			h.Add("Vary", "Origin")
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			if !p.preflightAllowed(r) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", p.allowMethods)
			h.Set("Access-Control-Allow-Headers", p.allowHeaders)
			h.Set("Access-Control-Max-Age", p.maxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The response differs by Origin, so caches must key on it.
		h.Add("Vary", "Origin")
		if slices.Contains(p.origins, origin) {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", p.exposeHeaders)
		}
		next.ServeHTTP(w, r)
	})
}

// preflightAllowed reports whether a preflight's origin, method and every
// requested header are allowed. Browsers send Access-Control-Request-Headers
// as one lowercase, comma-separated line, but proxies may split or reformat
// it, so every line is read and names are compared without case.
func (p *corsPolicy) preflightAllowed(r *http.Request) bool {
	if !slices.Contains(p.origins, r.Header.Get("Origin")) ||
		!slices.Contains(p.methods, r.Header.Get("Access-Control-Request-Method")) {
		return false
	}
	for _, line := range r.Header.Values("Access-Control-Request-Headers") {
		for name := range strings.SplitSeq(line, ",") {
			name = strings.TrimSpace(name)
			if name != "" && !slices.ContainsFunc(p.headers, func(allowed string) bool { return strings.EqualFold(allowed, name) }) {
				return false
			}
		}
	}
	return true
}
