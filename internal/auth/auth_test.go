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

func (d *devices) PairedDeviceActive(_ context.Context, id string) (bool, error) {
	if d.broken != nil {
		return false, d.broken
	}
	for _, known := range d.byHash {
		if known == id {
			return !d.revoked[id], nil
		}
	}
	return false, nil
}

func (d *devices) TouchPairedDevice(context.Context, string, time.Time) error {
	d.touches++
	return nil
}

// authenticate is Authenticate for requests whose check must not fail.
func authenticate(t *testing.T, a *Authenticator, r *http.Request) Principal {
	t.Helper()
	p, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newCode(t *testing.T, a *Authenticator, purpose Purpose) string {
	t.Helper()
	code, _, err := a.NewLoginCode(purpose)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestPairing(t *testing.T) {
	clock := t0
	store := newDevices()
	a := New("agent-token", sessionTTL, false, store, func() time.Time { return clock })
	code := newCode(t, a, PurposePair)
	id, token, err := a.Pair(context.Background(), code, "Laptop")
	if err != nil || id == "" || len(token) < 40 {
		t.Fatalf("Pair = %q, %q, %v", id, token, err)
	}
	if _, _, err := a.Pair(context.Background(), code, "Again"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code = %v, want ErrInvalidCode", err)
	}

	if got := authenticate(t, a, request("Bearer "+token, "")); got.Kind != KindDevice || got.ID != id {
		t.Fatalf("device token = %+v, want device %s", got, id)
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
	if got := authenticate(t, a, request("Bearer "+token, "")); got.Kind != KindNone {
		t.Fatalf("revoked device = %+v, want KindNone", got)
	}

	store.broken = errors.New("disk gone")
	if _, err := a.Authenticate(request("Bearer "+token, "")); err == nil {
		t.Fatal("an unreadable device store must fail the check, not deny or admit")
	}
	if got := authenticate(t, a, request("Bearer agent-token", "")); got.Kind != KindBearer {
		t.Fatal("the agent token must not depend on the device store")
	}
}

// TestCodesHaveAPurpose: a sign-in code (homecloud open, shown on screens)
// must not mint a device token that never expires, and the reverse.
func TestCodesHaveAPurpose(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	signIn, pair := newCode(t, a, PurposeSignIn), newCode(t, a, PurposePair)
	if _, _, err := a.Pair(context.Background(), signIn, "Laptop"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("pairing with a sign-in code = %v", err)
	}
	if _, err := a.Exchange(pair); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("signing in with a pairing code = %v", err)
	}
	// Refused for the wrong purpose, each still works for its own.
	if _, err := a.Exchange(signIn); err != nil {
		t.Fatalf("the sign-in code after a pairing attempt: %v", err)
	}
	if _, _, err := a.Pair(context.Background(), pair, "Laptop"); err != nil {
		t.Fatalf("the pairing code after a sign-in attempt: %v", err)
	}
	if _, _, err := a.NewLoginCode(Purpose(0)); err == nil {
		t.Fatal("a code without a purpose was issued")
	}
}

func TestSessionCookieIsSecureBehindHTTPS(t *testing.T) {
	clock := t0
	for _, secure := range []bool{false, true} {
		a := New("token", sessionTTL, secure, newDevices(), func() time.Time { return clock })
		cookie, err := a.Exchange(newCode(t, a, PurposeSignIn))
		if err != nil {
			t.Fatal(err)
		}
		if cookie.Secure != secure || a.ClearedCookie().Secure != secure {
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
		if got := authenticate(t, a, request(h, "")); got.Kind != want {
			t.Errorf("Authorization %q = %v, want %v", h, got.Kind, want)
		}
	}
}

func TestLoginCodeFlow(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	code, exp, err := a.NewLoginCode(PurposeSignIn)
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
	p := authenticate(t, a, request("", cookie.Value))
	if p.Kind != KindSession || p.ID == "" || p.ID == cookie.Value {
		t.Fatalf("session auth = %+v; its ID must name the session without being its cookie", p)
	}
	if got := authenticate(t, a, request("Bearer wrong", cookie.Value)); got.Kind != KindNone {
		t.Fatalf("a wrong bearer token must not fall back to the cookie, got %+v", got)
	}
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code = %v, want ErrInvalidCode", err)
	}
	clock = clock.Add(sessionTTL)
	if got := authenticate(t, a, request("", cookie.Value)); got.Kind != KindNone {
		t.Fatalf("expired session = %+v", got)
	}
}

func TestLoginCodeExpires(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	code := newCode(t, a, PurposeSignIn)
	clock = clock.Add(CodeTTL)
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code = %v, want ErrInvalidCode", err)
	}
}

// TestWrongCodesAreRateLimited: wrong guesses pause every attempt for a while
// but never discard the owner's code, which anyone could otherwise use to
// keep the owner from signing in.
func TestWrongCodesAreRateLimited(t *testing.T) {
	clock := t0
	a, _ := newAuth(t, &clock)
	code := newCode(t, a, PurposeSignIn)
	for range maxFailures {
		if _, err := a.Exchange("AAAA-AAAA"); !errors.Is(err, ErrInvalidCode) {
			t.Fatal(err)
		}
	}
	if _, err := a.Exchange(code); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("the right code during the pause = %v, want ErrTooManyAttempts", err)
	}
	clock = clock.Add(failureWindow)
	if _, err := a.Exchange(code); err != nil {
		t.Fatalf("the right code after the pause = %v", err)
	}
}

// TestValidEndsWithAccess: what outlives a request (a stream, a media URL)
// stops working when the device is revoked or the session ends.
func TestValidEndsWithAccess(t *testing.T) {
	clock := t0
	store := newDevices()
	a := New("agent-token", sessionTTL, false, store, func() time.Time { return clock })
	ctx := context.Background()
	valid := func(p Principal) bool {
		t.Helper()
		ok, err := a.Valid(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	agent := authenticate(t, a, request("Bearer agent-token", ""))
	if !valid(agent) || valid(Principal{Kind: KindBearer, ID: "someone"}) {
		t.Fatal("the agent token is the only bearer principal")
	}

	id, token, err := a.Pair(ctx, newCode(t, a, PurposePair), "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	device := authenticate(t, a, request("Bearer "+token, ""))
	if !valid(device) {
		t.Fatal("a paired device is not valid")
	}
	store.revoked[id] = true
	if valid(device) {
		t.Fatal("a revoked device is still valid")
	}

	var sessions []Principal
	for range 3 {
		cookie, exchangeErr := a.Exchange(newCode(t, a, PurposeSignIn))
		if exchangeErr != nil {
			t.Fatal(exchangeErr)
		}
		sessions = append(sessions, authenticate(t, a, request("", cookie.Value)))
	}
	if a.Sessions() != 3 || !valid(sessions[0]) {
		t.Fatalf("sessions = %d", a.Sessions())
	}
	if !a.SignOut(sessions[0].ID) || valid(sessions[0]) || a.SignOut(sessions[0].ID) {
		t.Fatal("a signed-out session is still valid, or signed out twice")
	}
	if n := a.SignOutAll(); n != 2 || valid(sessions[1]) || a.Sessions() != 0 {
		t.Fatalf("SignOutAll ended %d sessions; %d left", n, a.Sessions())
	}

	cookie, err := a.Exchange(newCode(t, a, PurposeSignIn))
	if err != nil {
		t.Fatal(err)
	}
	late := authenticate(t, a, request("", cookie.Value))
	clock = clock.Add(sessionTTL)
	if valid(late) {
		t.Fatal("an expired session is still valid")
	}
	if c := a.ClearedCookie(); c.MaxAge >= 0 || c.Value != "" || c.Name != SessionCookie {
		t.Fatalf("cleared cookie = %+v", c)
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
