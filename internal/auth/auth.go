// Package auth authenticates clients of the agent's local API.
//
// Three credentials exist:
//
//   - the agent token, generated at every agent start and published to
//     same-user clients on the agent's host (CLI, desktop app) through an
//     owner-only discovery file;
//   - a browser session cookie, obtained by exchanging a short-lived,
//     single-use login code that only an agent-token holder can create;
//   - a device token, obtained by pairing a device (the desktop app on
//     another computer) with such a login code. Only its SHA-256 is stored,
//     and it lasts until the device is revoked.
//
// Sessions live in memory and end when the agent restarts.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/vasilistotskas/homecloud/internal/ledger"
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
	// seenEvery bounds how often a device's last-seen time is written: its
	// precision in the app's device list, not a setting.
	seenEvery = time.Minute
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
	// KindBearer is the agent token.
	KindBearer
	KindSession
	// KindDevice is a paired device's token.
	KindDevice
)

// Devices keeps paired devices, by the SHA-256 of their tokens (package ledger).
type Devices interface {
	AddPairedDevice(ctx context.Context, id, name string, tokenSHA256 [32]byte, now time.Time) error
	// PairedDeviceByToken returns the device's id, or an error wrapping
	// ledger.ErrNotFound for a token of no device or of a revoked one.
	PairedDeviceByToken(ctx context.Context, tokenSHA256 [32]byte) (string, error)
	TouchPairedDevice(ctx context.Context, id string, now time.Time) error
}

// NewToken returns a random 256-bit bearer token.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Authenticator validates credentials and manages login codes, sessions and
// device pairing.
type Authenticator struct {
	token      []byte
	sessionTTL time.Duration
	secure     bool
	devices    Devices
	now        func() time.Time

	mu       sync.Mutex
	codes    map[string]time.Time // code → expiry
	sessions map[string]time.Time // session id → expiry
	seen     map[string]time.Time // device id → last-seen time last written
	failures int
}

// New returns an Authenticator accepting token as the agent token, whose
// browser sessions last sessionTTL and whose paired devices are kept in
// devices. secure marks session cookies Secure, for an agent served over
// https.
func New(token string, sessionTTL time.Duration, secure bool, devices Devices, now func() time.Time) *Authenticator {
	return &Authenticator{
		token:      []byte(token),
		sessionTTL: sessionTTL,
		secure:     secure,
		devices:    devices,
		now:        now,
		codes:      map[string]time.Time{},
		sessions:   map[string]time.Time{},
		seen:       map[string]time.Time{},
	}
}

// Authenticate reports how r is authenticated: a bearer token (the agent's
// or a paired device's) wins over a session cookie, and an invalid bearer
// token is never rescued by a cookie. It fails only when the device store
// cannot be read.
func (a *Authenticator) Authenticate(r *http.Request) (Kind, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return KindNone, nil
		}
		if subtle.ConstantTimeCompare([]byte(tok), a.token) == 1 {
			return KindBearer, nil
		}
		return a.device(r.Context(), tok)
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return KindNone, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	if !ok {
		return KindNone, nil
	}
	if !a.now().Before(exp) {
		delete(a.sessions, c.Value)
		return KindNone, nil
	}
	return KindSession, nil
}

// device authenticates a paired device's token. The store holds only token
// hashes, so the lookup reveals nothing about tokens.
func (a *Authenticator) device(ctx context.Context, token string) (Kind, error) {
	id, err := a.devices.PairedDeviceByToken(ctx, sha256.Sum256([]byte(token)))
	if errors.Is(err, ledger.ErrNotFound) {
		return KindNone, nil
	}
	if err != nil {
		return KindNone, fmt.Errorf("authenticate device: %w", err)
	}
	now := a.now()
	a.mu.Lock()
	due := now.Sub(a.seen[id]) >= seenEvery
	if due {
		a.seen[id] = now
	}
	a.mu.Unlock()
	if due {
		if err := a.devices.TouchPairedDevice(ctx, id, now); err != nil {
			return KindNone, fmt.Errorf("authenticate device: %w", err)
		}
	}
	return KindDevice, nil
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

// consume uses up a login code. Codes are single-use; too many wrong codes
// revoke all codes. a.mu must be held.
func (a *Authenticator) consume(code string) error {
	key := normalize(code)
	a.prune()
	if _, ok := a.codes[key]; !ok {
		a.failures++
		if a.failures >= maxFailedExchanges {
			clear(a.codes)
			a.failures = 0
		}
		return ErrInvalidCode
	}
	delete(a.codes, key)
	a.failures = 0
	return nil
}

// Pair trades a login code for a new device token, kept by the device: the
// agent stores only its SHA-256.
func (a *Authenticator) Pair(ctx context.Context, code, name string) (id, token string, err error) {
	a.mu.Lock()
	err = a.consume(code)
	a.mu.Unlock()
	if err != nil {
		return "", "", err
	}
	if token, err = NewToken(); err != nil {
		return "", "", err
	}
	id = uuid.New().String()
	if err := a.devices.AddPairedDevice(ctx, id, name, sha256.Sum256([]byte(token)), a.now()); err != nil {
		return "", "", fmt.Errorf("pair device: %w", err)
	}
	return id, token, nil
}

// Exchange trades a login code for a new session and returns the session
// cookie to set.
func (a *Authenticator) Exchange(code string) (*http.Cookie, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.consume(code); err != nil {
		return nil, err
	}
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
