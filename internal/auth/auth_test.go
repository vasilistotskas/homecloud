package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

const sessionTTL = 7 * 24 * time.Hour

func newAuth(t *testing.T, clock *time.Time) (*Authenticator, string) {
	t.Helper()
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return New(tok, sessionTTL, false, newDevices(), func() time.Time { return *clock }), tok
}

// devices is an in-memory Devices.
type devices struct {
	byHash  map[[32]byte]string
	revoked map[string]bool
	touches int
	broken  error
}

func newDevices() *devices {
	return &devices{byHash: map[[32]byte]string{}, revoked: map[string]bool{}}
}

func (d *devices) AddPairedDevice(_ context.Context, id, _ string, sum [32]byte, _ time.Time) error {
	d.byHash[sum] = id
	return nil
}

func (d *devices) PairedDeviceByToken(_ context.Context, sum [32]byte) (string, error) {
	if d.broken != nil {
		return "", d.broken
	}
	id, ok := d.byHash[sum]
	if !ok || d.revoked[id] {
		return "", ledger.ErrNotFound
	}
	return id, nil
}

func (d *devices) TouchPairedDevice(context.Context, string, time.Time) error {
	d.touches++
	return nil
}

// authenticate is Authenticate for requests whose check must not fail.
func authenticate(t *testing.T, a *Authenticator, r *http.Request) Kind {
	t.Helper()
	k, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPairing(t *testing.T) {
	clock := t0
	store := newDevices()
	a := New("agent-token", sessionTTL, false, store, func() time.Time { return clock })
	code, _, err := a.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	id, token, err := a.Pair(context.Background(), code, "Laptop")
	if err != nil || id == "" || len(token) < 40 {
		t.Fatalf("Pair = %q, %q, %v", id, token, err)
	}
	if _, _, err := a.Pair(context.Background(), code, "Again"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code = %v, want ErrInvalidCode", err)
	}
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("a code used for pairing must not also open a session")
	}

	if got := authenticate(t, a, request("Bearer "+token, "")); got != KindDevice {
		t.Fatalf("device token = %v, want KindDevice", got)
	}
	authenticate(t, a, request("Bearer "+token, ""))
	if store.touches != 1 {
		t.Fatalf("touches = %d; last-seen is written at most every %s", store.touches, seenEvery)
	}
	clock = clock.Add(seenEvery)
	authenticate(t, a, request("Bearer "+token, ""))
	if store.touches != 2 {
		t.Fatalf("touches after %s = %d, want 2", seenEvery, store.touches)
	}

	store.revoked[id] = true
	if got := authenticate(t, a, request("Bearer "+token, "")); got != KindNone {
		t.Fatalf("revoked device = %v, want KindNone", got)
	}

	store.broken = errors.New("disk gone")
	if _, err := a.Authenticate(request("Bearer "+token, "")); err == nil {
		t.Fatal("an unreadable device store must fail the check, not deny or admit")
	}
	if got := authenticate(t, a, request("Bearer agent-token", "")); got != KindBearer {
		t.Fatal("the agent token must not depend on the device store")
	}
}

func TestSessionCookieIsSecureBehindHTTPS(t *testing.T) {
	clock := t0
	for _, secure := range []bool{false, true} {
		a := New("token", sessionTTL, secure, newDevices(), func() time.Time { return clock })
		code, _, err := a.NewLoginCode()
		if err != nil {
			t.Fatal(err)
		}
		cookie, err := a.Exchange(code)
		if err != nil {
			t.Fatal(err)
		}
		if cookie.Secure != secure {
			t.Errorf("secure=%v: cookie.Secure = %v", secure, cookie.Secure)
		}
	}
}

func request(header, cookie string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie}) //nolint:gosec // request-side test cookie
	}
	return r
}

func TestBearer(t *testing.T) {
	clock := t0
	a, tok := newAuth(t, &clock)
	if len(tok) != 43 {
		t.Fatalf("token length = %d, want 43 (256 bits, base64url)", len(tok))
	}
	cases := map[string]Kind{
		"Bearer " + tok:       KindBearer,
		"Bearer " + tok + "x": KindNone,
		"bearer " + tok:       KindNone,
		"Basic " + tok:        KindNone,
		"":                    KindNone,
	}
	for h, want := range cases {
		if got := authenticate(t, a, request(h, "")); got != want {
			t.Errorf("Authorization %q = %v, want %v", h, got, want)
		}
	}
}

func TestLoginCodeFlow(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	code, exp, err := a.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 9 || code[4] != '-' || strings.ContainsAny(code, "01ILO") || !exp.Equal(t0.Add(CodeTTL)) {
		t.Fatalf("code = %q expires %v", code, exp)
	}
	cookie, err := a.Exchange(strings.ToLower(strings.ReplaceAll(code, "-", "")))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("cookie attributes = %+v", cookie)
	}
	if got := authenticate(t, a, request("", cookie.Value)); got != KindSession {
		t.Fatalf("session auth = %v", got)
	}
	if got := authenticate(t, a, request("Bearer wrong", cookie.Value)); got != KindNone {
		t.Fatalf("a wrong bearer token must not fall back to the cookie, got %v", got)
	}
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code = %v, want ErrInvalidCode", err)
	}
	clock = clock.Add(sessionTTL)
	if got := authenticate(t, a, request("", cookie.Value)); got != KindNone {
		t.Fatalf("expired session = %v", got)
	}
}

func TestLoginCodeExpires(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	code, _, err := a.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(CodeTTL)
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code = %v, want ErrInvalidCode", err)
	}
}

func TestWrongGuessesRevokeCodes(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	code, _, err := a.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	for range maxFailedExchanges {
		if _, err := a.Exchange("AAAA-AAAA"); !errors.Is(err, ErrInvalidCode) {
			t.Fatal(err)
		}
	}
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("code after %d wrong guesses = %v, want revoked", maxFailedExchanges, err)
	}
}

func TestCodesAreUniform(t *testing.T) {
	seen := map[rune]int{}
	for range 2000 {
		c, err := randomCode()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range c {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("code %q has %q outside the alphabet", c, r)
			}
			seen[r]++
		}
	}
	if len(seen) != len(codeAlphabet) {
		t.Fatalf("saw %d distinct characters, want %d", len(seen), len(codeAlphabet))
	}
}
