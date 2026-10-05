package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newAuth(t *testing.T, clock *time.Time) (*Authenticator, string) {
	t.Helper()
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return New(tok, func() time.Time { return *clock }), tok
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
		if got := a.Authenticate(request(h, "")); got != want {
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
	if got := a.Authenticate(request("", cookie.Value)); got != KindSession {
		t.Fatalf("session auth = %v", got)
	}
	if got := a.Authenticate(request("Bearer wrong", cookie.Value)); got != KindNone {
		t.Fatalf("a wrong bearer token must not fall back to the cookie, got %v", got)
	}
	if _, err := a.Exchange(code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code = %v, want ErrInvalidCode", err)
	}
	clock = clock.Add(SessionTTL)
	if got := a.Authenticate(request("", cookie.Value)); got != KindNone {
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
