// Package auth authenticates clients of the agent's local API.
//
// Three credentials exist:
//
//   - the agent token, generated at every agent start and published to
//     same-user clients on the agent's host (CLI, desktop app) through an
//     owner-only discovery file;
//   - a browser session cookie, obtained by exchanging a short-lived,
//     single-use sign-in code that only an agent-token holder can create;
//   - a device token, obtained by pairing a device (the desktop app on
//     another computer) with a pairing code. Only its SHA-256 is stored, and
//     it lasts until the device is revoked.
//
// A request authenticates as a Principal, which Valid re-checks for whatever
// outlives the request (an open stream, a media URL): revoking a device or
// ending a session ends its access at once. Sessions live in memory and end
// when the agent restarts.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

const (
	tokenBytes = 32
	// handleBytes sizes a session's handle: it names the session (in media
	// URLs) and is no secret, but must not repeat.
	handleBytes = 16
	// SessionCookie is the name of the browser session cookie.
	SessionCookie = "homecloud_session"
	// CodeTTL is how long a login code can be used.
	CodeTTL = 2 * time.Minute
	// Wrong codes are limited to maxFailures in failureWindow, all callers
	// together. Codes are never discarded for them: anyone who can reach the
	// agent could then keep the owner from signing in. Eight characters of a
	// 31-letter alphabet leave a guess no chance at this rate.
	maxFailures   = 5
	failureWindow = time.Minute
	// codeAlphabet omits look-alike characters (0/O, 1/I/L).
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	codeLen      = 8
	// seenEvery bounds how often a device's last-seen time is written: its
	// precision in the app's device list, not a setting.
	seenEvery = time.Minute
	// agentID is the agent token's principal ID.
	agentID = "agent"
)

// Errors returned for login codes.
var (
	ErrInvalidCode     = errors.New("login code is invalid, expired or already used")
	ErrTooManyAttempts = errors.New("too many wrong login codes; wait a minute and try again")
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

// Purpose is what a login code may be traded for: a code made for one is
// refused for the other.
type Purpose int

// Code purposes.
const (
	// PurposeSignIn is a browser session (homecloud open).
	PurposeSignIn Purpose = iota + 1
	// PurposePair is a device token, which lasts until the device is revoked
	// (homecloud pair).
	PurposePair
)

// Principal is who a request authenticated as.
type Principal struct {
	Kind Kind
	// ID names the principal and is no secret: "agent" for the agent token,
	// the device's id, or the session's handle (never its cookie).
	ID string
}

// principalKinds are the letters that name a principal's kind in String.
var principalKinds = map[Kind]string{KindBearer: "a", KindSession: "s", KindDevice: "d"}

// String encodes p for a URL (media links carry who they were made for); it
// holds nothing secret.
func (p Principal) String() string { return principalKinds[p.Kind] + ":" + p.ID }

// LogValue is how logs name p: its String, or "none" for a request no
// credential came with.
func (p Principal) LogValue() slog.Value {
	if p.Kind == KindNone {
		return slog.StringValue("none")
	}
	return slog.StringValue(p.String())
}

// ParsePrincipal decodes what String encoded.
func ParsePrincipal(s string) (Principal, error) {
	letter, id, ok := strings.Cut(s, ":")
	for kind, l := range principalKinds {
		if ok && l == letter && id != "" {
			return Principal{Kind: kind, ID: id}, nil
		}
	}
	return Principal{}, fmt.Errorf("malformed principal %q", s)
}

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal ctx carries.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Devices keeps paired devices, by the SHA-256 of their tokens (package ledger).
type Devices interface {
	AddPairedDevice(ctx context.Context, id, name string, tokenSHA256 [32]byte, now time.Time) error
	// PairedDeviceByToken returns the device's id, or an error wrapping
	// ledger.ErrNotFound for a token of no device or of a revoked one.
	PairedDeviceByToken(ctx context.Context, tokenSHA256 [32]byte) (string, error)
	PairedDeviceActive(ctx context.Context, id string) (bool, error)
	TouchPairedDevice(ctx context.Context, id string, now time.Time) error
}

// NewToken returns a random 256-bit bearer token.
func NewToken() (string, error) {
	return randomString(tokenBytes)
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type loginCode struct {
	expires time.Time
	purpose Purpose
}

type session struct {
	handle  string
	expires time.Time
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
	codes    map[string]loginCode // normalized code → its expiry and purpose
	sessions map[string]session   // cookie value → session
	failures []time.Time          // wrong codes within failureWindow
	seen     map[string]time.Time // device id → last-seen time last written
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
		codes:      map[string]loginCode{},
		sessions:   map[string]session{},
		seen:       map[string]time.Time{},
	}
}

// Authenticate reports who r authenticated as: a bearer token (the agent's
// or a paired device's) wins over a session cookie, and an invalid bearer
// token is never rescued by a cookie. KindNone means no valid credential. It
// fails only when the device store cannot be read.
func (a *Authenticator) Authenticate(r *http.Request) (Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return Principal{}, nil
		}
		if subtle.ConstantTimeCompare([]byte(tok), a.token) == 1 {
			return Principal{Kind: KindBearer, ID: agentID}, nil
		}
		return a.device(r.Context(), tok)
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return Principal{}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok {
		return Principal{}, nil
	}
	if !a.now().Before(s.expires) {
		delete(a.sessions, c.Value)
		return Principal{}, nil
	}
	return Principal{Kind: KindSession, ID: s.handle}, nil
}

// device authenticates a paired device's token. The store holds only token
// hashes, so the lookup reveals nothing about tokens.
func (a *Authenticator) device(ctx context.Context, token string) (Principal, error) {
	id, err := a.devices.PairedDeviceByToken(ctx, sha256.Sum256([]byte(token)))
	if errors.Is(err, ledger.ErrNotFound) {
		return Principal{}, nil
	}
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate device: %w", err)
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
			return Principal{}, fmt.Errorf("authenticate device: %w", err)
		}
	}
	return Principal{Kind: KindDevice, ID: id}, nil
}

// Valid reports whether p may still act. A principal checked when its
// request began may have lost that right since: a device revoked, a session
// ended or expired.
func (a *Authenticator) Valid(ctx context.Context, p Principal) (bool, error) {
	switch p.Kind {
	case KindBearer:
		return p.ID == agentID, nil
	case KindSession:
		now := a.now()
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, s := range a.sessions {
			if s.handle == p.ID {
				return now.Before(s.expires), nil
			}
		}
		return false, nil
	case KindDevice:
		ok, err := a.devices.PairedDeviceActive(ctx, p.ID)
		if err != nil {
			return false, fmt.Errorf("check device: %w", err)
		}
		return ok, nil
	case KindNone:
	}
	return false, nil
}

// NewLoginCode issues a single-use login code for purpose, formatted
// XXXX-XXXX.
func (a *Authenticator) NewLoginCode(purpose Purpose) (code string, expires time.Time, err error) {
	if purpose != PurposeSignIn && purpose != PurposePair {
		return "", time.Time{}, fmt.Errorf("unknown login code purpose %d", purpose)
	}
	raw, err := randomCode()
	if err != nil {
		return "", time.Time{}, err
	}
	code = raw[:codeLen/2] + "-" + raw[codeLen/2:]
	expires = a.now().Add(CodeTTL)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	a.codes[normalize(code)] = loginCode{expires: expires, purpose: purpose}
	return code, expires, nil
}

// consume uses up a login code made for purpose. A code made for another
// purpose is refused and left for its own use. a.mu must be held.
func (a *Authenticator) consume(code string, purpose Purpose) error {
	a.prune()
	if len(a.failures) >= maxFailures {
		return ErrTooManyAttempts
	}
	key := normalize(code)
	c, ok := a.codes[key]
	if !ok || c.purpose != purpose {
		a.failures = append(a.failures, a.now())
		return ErrInvalidCode
	}
	delete(a.codes, key)
	return nil
}

// Pair trades a pairing code for a new device token, kept by the device: the
// agent stores only its SHA-256.
func (a *Authenticator) Pair(ctx context.Context, code, name string) (id, token string, err error) {
	a.mu.Lock()
	err = a.consume(code, PurposePair)
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

// Exchange trades a sign-in code for a new session and returns the session
// cookie to set.
func (a *Authenticator) Exchange(code string) (*http.Cookie, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.consume(code, PurposeSignIn); err != nil {
		return nil, err
	}
	id, err := NewToken()
	if err != nil {
		return nil, err
	}
	handle, err := randomString(handleBytes)
	if err != nil {
		return nil, err
	}
	a.sessions[id] = session{handle: handle, expires: a.now().Add(a.sessionTTL)}
	return a.cookie(id, int(a.sessionTTL/time.Second)), nil
}

// SignOut ends the session with handle; it reports whether there was one.
func (a *Authenticator) SignOut(handle string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sessions {
		if s.handle == handle {
			delete(a.sessions, id)
			return true
		}
	}
	return false
}

// SignOutAll ends every browser session and returns how many there were.
func (a *Authenticator) SignOutAll() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	n := len(a.sessions)
	clear(a.sessions)
	return n
}

// Sessions returns how many browser sessions are open.
func (a *Authenticator) Sessions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prune()
	return len(a.sessions)
}

// ClearedCookie is the cookie that makes a browser drop its session.
func (a *Authenticator) ClearedCookie() *http.Cookie {
	return a.cookie("", -1)
}

// cookie is the session cookie with value, kept maxAge seconds (-1: drop it).
func (a *Authenticator) cookie(value string, maxAge int) *http.Cookie {
	// Secure only behind an https proxy: on plain-HTTP loopback browsers do
	// not uniformly accept Secure cookies, and SameSite=Strict, HttpOnly and
	// the Host/Origin checks in package web protect the session instead; the
	// app's own host name keeps it from other loopback services.
	return &http.Cookie{ //nolint:gosec // G124: see above
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
		Secure:   a.secure,
	}
}

// prune drops expired codes and sessions, and wrong codes older than
// failureWindow; a.mu must be held.
func (a *Authenticator) prune() {
	now := a.now()
	for k, c := range a.codes {
		if !now.Before(c.expires) {
			delete(a.codes, k)
		}
	}
	for k, s := range a.sessions {
		if !now.Before(s.expires) {
			delete(a.sessions, k)
		}
	}
	recent := a.failures[:0]
	for _, at := range a.failures {
		if now.Sub(at) < failureWindow {
			recent = append(recent, at)
		}
	}
	a.failures = recent
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
