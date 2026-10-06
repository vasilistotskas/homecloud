package web

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const desktop = "http://tauri.localhost"

func testPolicy() *corsPolicy {
	return newCORSPolicy(
		[]string{desktop, "tauri://localhost"},
		[]string{"GET", "POST"},
		[]string{"Content-Type", "Connect-Protocol-Version", "Authorization"},
		[]string{"Grpc-Status", "Grpc-Message"},
		2*time.Hour,
	)
}

// serve runs one request through the policy; reached reports whether the
// wrapped handler ran.
func serve(t *testing.T, method string, header http.Header) (res *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequestWithContext(t.Context(), method, "/homecloud.agent.v3.AgentService/GetInfo", nil)
	req.Header = header
	res = httptest.NewRecorder()
	testPolicy().handler(next).ServeHTTP(res, req)
	return res, reached
}

func corsHeaders(h http.Header) []string {
	var names []string
	for name := range h {
		if strings.HasPrefix(name, "Access-Control-") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func TestPreflight(t *testing.T) {
	preflight := func(origin, method string, requestHeaders ...string) http.Header {
		h := http.Header{"Origin": {origin}, "Access-Control-Request-Method": {method}}
		for _, v := range requestHeaders {
			h.Add("Access-Control-Request-Headers", v)
		}
		return h
	}
	cases := []struct {
		name   string
		header http.Header
		allow  bool
	}{
		{"as WebView2 sends it", preflight(desktop, "POST", "authorization,connect-protocol-version,content-type"), true},
		{"other desktop origin", preflight("tauri://localhost", "POST", "content-type"), true},
		{"no headers requested", preflight(desktop, "GET"), true},
		{"empty headers line", preflight(desktop, "POST", ""), true},
		{"names in any case, with spaces", preflight(desktop, "POST", "Authorization, Content-Type"), true},
		{"names split over lines", preflight(desktop, "POST", "authorization", "content-type"), true},
		{"unlisted header", preflight(desktop, "POST", "authorization,x-evil"), false},
		{"unlisted header on a second line", preflight(desktop, "POST", "authorization", "x-evil"), false},
		{"unlisted method", preflight(desktop, "DELETE"), false},
		{"method in another case", preflight(desktop, "post"), false},
		{"origin not allowed", preflight("http://127.0.0.1:8790", "POST", "content-type"), false},
		{"origin differing in case", preflight("HTTP://TAURI.LOCALHOST", "POST"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, reached := serve(t, http.MethodOptions, c.header)
			if reached {
				t.Fatal("a preflight reached the API")
			}
			vary := res.Header().Values("Vary")
			for _, want := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
				if !slices.Contains(vary, want) {
					t.Errorf("Vary = %q, missing %s", vary, want)
				}
			}
			if !c.allow {
				if res.Code != http.StatusForbidden || len(corsHeaders(res.Header())) != 0 {
					t.Fatalf("refused preflight = %d with %v; want 403 and no CORS headers", res.Code, corsHeaders(res.Header()))
				}
				return
			}
			h := res.Header()
			if res.Code != http.StatusNoContent ||
				h.Get("Access-Control-Allow-Origin") != c.header.Get("Origin") ||
				h.Get("Access-Control-Allow-Methods") != "GET, POST" ||
				h.Get("Access-Control-Allow-Headers") != "Content-Type, Connect-Protocol-Version, Authorization" ||
				h.Get("Access-Control-Max-Age") != "7200" {
				t.Fatalf("allowed preflight = %d %v", res.Code, h)
			}
			if got := corsHeaders(h); !slices.Equal(got, []string{
				"Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Access-Control-Allow-Origin", "Access-Control-Max-Age",
			}) {
				t.Fatalf("CORS headers = %v; no credentials, no private-network grant", got)
			}
		})
	}
}

func TestActualRequests(t *testing.T) {
	cases := []struct {
		name   string
		method string
		header http.Header
		cors   bool
	}{
		{"from the desktop app", http.MethodPost, http.Header{"Origin": {desktop}}, true},
		{"from the other desktop origin", http.MethodGet, http.Header{"Origin": {"tauri://localhost"}}, true},
		{"same-origin with Origin", http.MethodPost, http.Header{"Origin": {"http://127.0.0.1:8790"}}, false},
		{"without Origin", http.MethodGet, http.Header{}, false},
		// An OPTIONS request that is not a preflight is the API's to answer.
		{"OPTIONS without a request method", http.MethodOptions, http.Header{"Origin": {desktop}}, true},
		{"OPTIONS without Origin", http.MethodOptions, http.Header{"Access-Control-Request-Method": {"POST"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, reached := serve(t, c.method, c.header)
			if !reached || res.Code != http.StatusTeapot {
				t.Fatalf("not passed through: reached=%v code=%d", reached, res.Code)
			}
			if !slices.Contains(res.Header().Values("Vary"), "Origin") {
				t.Errorf("Vary = %q, want Origin", res.Header().Values("Vary"))
			}
			h := res.Header()
			if !c.cors {
				if got := corsHeaders(h); len(got) != 0 {
					t.Fatalf("CORS headers %v on a request not from an allowed origin", got)
				}
				return
			}
			if h.Get("Access-Control-Allow-Origin") != c.header.Get("Origin") ||
				h.Get("Access-Control-Expose-Headers") != "Grpc-Status, Grpc-Message" {
				t.Fatalf("CORS headers = %v", h)
			}
			if got := corsHeaders(h); len(got) != 2 {
				t.Fatalf("CORS headers = %v; want only allow-origin and expose-headers", got)
			}
		})
	}
}
