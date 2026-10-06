// Package auth authenticates clients of the agent's local API.
//
// Two credentials exist:
//
//   - a bearer token, generated at every agent start and published to
//     same-user clients (CLI, desktop app) through an owner-only discovery
//     file;
//   - a browser session cookie, obtained by exchanging a short-lived,
//     single-use login code that only a bearer-token holder can create.
//
// Sessions live in memory and end when the agent restarts.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	tokenBytes = 32
	// SessionCookie is the name of the browser session cookie.
	SessionCookie = "homecloud_session"
	// CodeTTL is how long a login code can be exchanged.
	CodeTTL = 2 * time.Minute
	// maxFailedExchanges invalidates every outstanding code after this many
	// wrong guesses, so codes cannot be brute-forced.
	maxFailedExchanges = 5
	// codeAlphabet omits look-alike characters (0/O, 1/I/L).
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	codeLen      = 8
)

// Errors returned by Exchange.
var (
	ErrInvalidCode = errors.New("login code is invalid, expired or already used")
)

// Kind is how a request authenticated.
type Kind int

// Credential kinds.
const (
	KindNone Kind = iota
	KindBearer
	KindSession
)

// NewToken returns a random 256-bit bearer token.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Authenticator validates credentials and manages login codes and sessions.
type Authenticator struct {
	token      []byte
	sessionTTL time.Duration
	secure     bool
	now        func() time.Time

	mu       sync.Mutex
	codes    map[string]time.Time // code → expiry
	sessions map[string]time.Time // session id → expiry
	failures int
}

// New returns an Authenticator accepting token as the bearer credential,
// whose browser sessions last sessionTTL. secure marks session cookies
// Secure, for an agent served over https.
func New(token string, sessionTTL time.Duration, secure bool, now func() time.Time) *Authenticator {
	return &Authenticator{
		token:      []byte(token),
		sessionTTL: sessionTTL,
		secure:     secure,
		now:        now,
		codes:      map[string]time.Time{},
		sessions:   map[string]time.Time{},
	}
}

// Authenticate reports how r is authenticated: a valid bearer token wins over
// a session cookie; an invalid bearer token is never rescued by a cookie.
func (a *Authenticator) Authenticate(r *http.Request) Kind {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(tok), a.token) == 1 {
			return KindBearer
		}
		return KindNone
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return KindNone
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	if !ok {
		return KindNone
	}
	if !a.now().Before(exp) {
		delete(a.sessions, c.Value)
		return KindNone
	}
	return KindSession
}

// NewLoginCode issues a single-use login code, formatted XXXX-XXXX.
func (a *Authenticator) NewLoginCode() (code string, expires time.Time, err error) {
	raw, err := randomCode()
	if err != nil {
		return "", time.Time{}, err
	}
	code = raw[:codeLen/2] + "-" + raw[codeLen/2:]
	expires = a.now().Add(CodeTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	a.codes[normalize(code)] = expires
	return code, expires, nil
}

// Exchange trades a login code for a new session and returns the session
// cookie to set. Codes are single-use; too many wrong codes revoke all codes.
func (a *Authenticator) Exchange(code string) (*http.Cookie, error) {
	key := normalize(code)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	if _, ok := a.codes[key]; !ok {
		a.failures++
		if a.failures >= maxFailedExchanges {
			clear(a.codes)
			a.failures = 0
		}
		return nil, ErrInvalidCode
	}
	delete(a.codes, key)
	a.failures = 0
	id, err := NewToken()
	if err != nil {
		return nil, err
	}
	a.sessions[id] = a.now().Add(a.sessionTTL)
	// Secure only behind an https proxy: on plain-HTTP loopback browsers do
	// not uniformly accept Secure cookies, and SameSite=Strict, HttpOnly and
	// the Host/Origin checks in package web protect the session instead.
	return &http.Cookie{ //nolint:gosec // G124: see above
		Name:     SessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(a.sessionTTL / time.Second),
		Secure:   a.secure,
	}, nil
}

// prune drops expired codes and sessions; a.mu must be held.
func (a *Authenticator) prune() {
	now := a.now()
	for k, exp := range a.codes {
		if !now.Before(exp) {
			delete(a.codes, k)
		}
	}
	for k, exp := range a.sessions {
		if !now.Before(exp) {
			delete(a.sessions, k)
		}
	}
}

// randomCode draws codeLen characters uniformly from codeAlphabet, rejecting
// bytes that would bias the modulo.
func randomCode() (string, error) {
	const n = byte(len(codeAlphabet))
	limit := 256 / int(n) * int(n)
	out := make([]byte, 0, codeLen)
	buf := make([]byte, 2*codeLen)
	for len(out) < codeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generate login code: %w", err)
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < codeLen {
				out = append(out, codeAlphabet[b%n])
			}
		}
	}
	return string(out), nil
}

// normalize accepts codes typed with or without the dash, in any case.
func normalize(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
