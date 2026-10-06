// Package icloud signs in to iCloud as iCloud.com does and keeps that session,
// so homecloud can read iCloud web services (iCloud Drive) without storing the
// owner's password.
//
// The owner signs in once, interactively (StartLogin): SRP password proof,
// two-factor code, Apple's escrow proof when asked, trust. The resulting
// session (cookies, session and trust tokens, web service URLs) is saved owner
// only; the password is never kept. Unattended use refreshes that session
// (Refresh) and reports ErrSignInRequired once Apple wants the password or a
// code again.
//
// The protocol follows Apple's web client as implemented and live-tested by
// icloud-photos-sync (2026-10, including the escrow step) and rclone's
// iclouddrive backend.
package icloud

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// clientID is iCloud.com's public web client identifier (rclone and
// icloud-photos-sync send the same).
const clientID = "d39ba9916b7251055b22c7f910e2ea796ee65e98b2ddecea8f5dde8d9d1a815d"

// userAgent is a current desktop browser: Apple's endpoints serve web clients.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

const (
	requestTimeout = time.Minute
	// maxBody bounds a JSON response read into memory.
	maxBody = 32 << 20
)

// Web service keys of the account's service map.
const (
	// ServiceDrive lists and manages iCloud Drive items.
	ServiceDrive = "drivews"
	// ServiceDocs downloads iCloud Drive file contents.
	ServiceDocs = "docws"
)

// ErrSignInRequired means the saved session no longer works and the owner
// must run the interactive sign-in again.
var ErrSignInRequired = errors.New("iCloud sign-in required")

// Endpoints are Apple's service roots; tests point them at a local server.
type Endpoints struct {
	// Auth is the sign-in service (idmsa).
	Auth string
	// Setup issues the iCloud web session (accountLogin, validate).
	Setup string
	// Home is iCloud.com, the origin Apple's web services expect.
	Home string
}

// AppleEndpoints are the production endpoints.
var AppleEndpoints = Endpoints{
	Auth:  "https://idmsa.apple.com/appleauth/auth",
	Setup: "https://setup.icloud.com/setup/ws/1",
	Home:  "https://www.icloud.com",
}

// Session is the saved sign-in. It holds secrets: keep it owner-only and
// never log it.
type Session struct {
	AppleID string `json:"apple_id"`
	// SessionToken is Apple's dsWebAuthToken, exchanged for web cookies.
	SessionToken string `json:"session_token"`
	// TrustToken marks this client as a trusted browser, sparing a code.
	TrustToken     string         `json:"trust_token"`
	AccountCountry string         `json:"account_country"`
	Cookies        []cookieRecord `json:"cookies"`
	// Services maps web service keys (ServiceDrive, ...) to their endpoints.
	Services map[string]Service `json:"services"`
	// SignedInAt is when the owner last completed the interactive sign-in.
	SignedInAt time.Time `json:"signed_in_at"`
}

// Service is one iCloud web service of the account.
type Service struct {
	URL string `json:"url"`
	// PCSRequired is set under Advanced Data Protection: the service then
	// needs per-service cookies the owner approves on a trusted device.
	PCSRequired bool `json:"pcsRequired"`
}

// accountInfo is the part of accountLogin's and validate's answer homecloud uses.
type accountInfo struct {
	DsInfo struct {
		HsaVersion int `json:"hsaVersion"`
	} `json:"dsInfo"`
	HsaChallengeRequired bool               `json:"hsaChallengeRequired"`
	Webservices          map[string]Service `json:"webservices"`
}

// needsCode reports whether Apple still wants a two-factor code for this session.
func (a accountInfo) needsCode() bool {
	return a.DsInfo.HsaVersion == 2 && a.HsaChallengeRequired
}

// Client talks to iCloud with one session.
type Client struct {
	ep   Endpoints
	http *http.Client
	jar  *jar
	now  func() time.Time

	mu      sync.Mutex
	session Session
}

// New returns a client for the saved session s ({} before the first sign-in).
func New(ep Endpoints, s Session, now func() time.Time) (*Client, error) {
	j, err := restoreJar(s.Cookies, now)
	if err != nil {
		return nil, err
	}
	return &Client{
		ep: ep,
		// Calls carry tokens in their bodies: a redirect is never followed
		// (it ends as an unexpected status), so they reach only Apple's endpoints.
		http: &http.Client{
			Jar: j, Timeout: requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		jar:     j,
		now:     now,
		session: s,
	}, nil
}

// Session returns the session to save, with the current cookies.
func (c *Client) Session() Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.session
	s.Cookies = c.jar.save()
	return s
}

// Service returns the endpoint of a web service, or ErrSignInRequired when the
// session has none (never signed in).
func (c *Client) Service(key string) (Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.session.Services[key]
	if !ok || s.URL == "" {
		return Service{}, fmt.Errorf("%w: no %s service in the saved session", ErrSignInRequired, key)
	}
	return s, nil
}

// webHeaders are the headers Apple's web services expect from iCloud.com.
func (c *Client) webHeaders() http.Header {
	return http.Header{
		"Accept":       {"application/json"},
		"Content-Type": {"application/json"},
		"Origin":       {c.ep.Home},
		"Referer":      {c.ep.Home + "/"},
		"User-Agent":   {userAgent},
	}
}

// response is a read HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

// send performs one request with a JSON body (nil for none) and reads the
// whole response. Statuses are the caller's to judge.
func (c *Client) send(ctx context.Context, method, url string, header http.Header, body any) (response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, fmt.Errorf("encode %s request: %w", method, err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header = header
	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("%s %s: %w", method, endpointName(url), err)
	}
	defer resp.Body.Close() //nolint:errcheck // fully read below
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return response{}, fmt.Errorf("read %s: %w", endpointName(url), err)
	}
	if len(data) > maxBody {
		return response{}, fmt.Errorf("%s: response larger than %d bytes", endpointName(url), maxBody)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

// endpointName is a URL without its query, for messages: queries may carry
// tokens.
func endpointName(url string) string {
	name, _, _ := strings.Cut(url, "?")
	return name
}

// statusError describes an unexpected status without echoing the body, which
// may hold account details.
func statusError(what string, r response) error {
	return fmt.Errorf("%s: unexpected status %d", what, r.status)
}

func decode(what string, r response, v any) error {
	if err := json.Unmarshal(r.body, v); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	return nil
}

// accountLogin exchanges the session token for web cookies and the service map.
func (c *Client) accountLogin(ctx context.Context) error {
	c.mu.Lock()
	payload := map[string]any{
		"dsWebAuthToken":     c.session.SessionToken,
		"accountCountryCode": c.session.AccountCountry,
		"extended_login":     true,
		"trustToken":         c.session.TrustToken,
	}
	c.mu.Unlock()
	r, err := c.send(ctx, http.MethodPost, c.ep.Setup+"/accountLogin", c.webHeaders(), payload)
	if err != nil {
		return err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusMisdirectedRequest:
		return fmt.Errorf("%w: accountLogin was refused (%d)", ErrSignInRequired, r.status)
	default:
		return statusError("accountLogin", r)
	}
	var info accountInfo
	if err := decode("accountLogin", r, &info); err != nil {
		return err
	}
	return c.adopt(info)
}

// validate checks the session cookies and returns the account's service map.
func (c *Client) validate(ctx context.Context) error {
	r, err := c.send(ctx, http.MethodPost, c.ep.Setup+"/validate", c.webHeaders(), nil)
	if err != nil {
		return err
	}
	if r.status != http.StatusOK {
		return statusError("validate", r)
	}
	var info accountInfo
	if err := decode("validate", r, &info); err != nil {
		return err
	}
	return c.adopt(info)
}

// adopt takes the service map of a working session.
func (c *Client) adopt(info accountInfo) error {
	if info.needsCode() {
		return fmt.Errorf("%w: Apple asks for a two-factor code again", ErrSignInRequired)
	}
	if len(info.Webservices) == 0 {
		return errors.New("iCloud returned no web services for this account")
	}
	c.mu.Lock()
	c.session.Services = info.Webservices
	c.mu.Unlock()
	return nil
}

// Refresh renews the web session without the password: first with the saved
// cookies, then by exchanging the saved session and trust tokens again. It
// returns ErrSignInRequired when Apple wants the owner to sign in.
func (c *Client) Refresh(ctx context.Context) error {
	if err := c.validate(ctx); err == nil {
		return nil
	}
	c.mu.Lock()
	hasToken := c.session.SessionToken != ""
	c.mu.Unlock()
	if !hasToken {
		return fmt.Errorf("%w: never signed in", ErrSignInRequired)
	}
	return c.accountLogin(ctx)
}

// ErrStalled means a download received no data for its stall timeout.
var ErrStalled = errors.New("download stalled")

// Fetch streams the body of a signed content URL (a file download). A
// download has no overall deadline, since it lasts as long as the file needs;
// it is abandoned when no data arrives for stall. The caller closes the body.
func (c *Client) Fetch(ctx context.Context, rawURL string, stall time.Duration) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	watchdog := time.AfterFunc(stall, func() { cancel(ErrStalled) })
	fail := func(err error) (io.ReadCloser, error) {
		watchdog.Stop()
		cancel(nil)
		if errors.Is(context.Cause(ctx), ErrStalled) {
			err = fmt.Errorf("%w: no response for %s", ErrStalled, stall)
		}
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fail(fmt.Errorf("build download request: %w", err))
	}
	req.Header = c.webHeaders()
	req.Header.Del("Content-Type")
	req.Header.Set("Accept", "*/*")
	// Content servers may redirect a download; it carries no tokens.
	download := *c.http
	download.Timeout = 0
	download.CheckRedirect = nil
	resp, err := download.Do(req)
	if err != nil {
		return fail(fmt.Errorf("GET %s: %w", endpointName(rawURL), err))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close() //nolint:errcheck,gosec // the status is the error
		return fail(fmt.Errorf("GET %s: unexpected status %d", endpointName(rawURL), resp.StatusCode))
	}
	return &watchedBody{body: resp.Body, ctx: ctx, cancel: cancel, watchdog: watchdog, stall: stall}, nil
}

// watchedBody is a download whose watchdog is reset by every read that
// returns data.
type watchedBody struct {
	body     io.ReadCloser
	ctx      context.Context //nolint:containedctx // the download's own context, for its cause
	cancel   context.CancelCauseFunc
	watchdog *time.Timer
	stall    time.Duration
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.watchdog.Reset(b.stall)
	}
	if err != nil && !errors.Is(err, io.EOF) && errors.Is(context.Cause(b.ctx), ErrStalled) {
		return n, fmt.Errorf("%w: no data for %s", ErrStalled, b.stall)
	}
	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must stay unwrapped
}

func (b *watchedBody) Close() error {
	b.watchdog.Stop()
	b.cancel(nil)
	return b.body.Close() //nolint:wrapcheck // io.Closer of the response body
}

// Call sends a JSON request to a web service and decodes a 200 answer into
// out. An expired session is refreshed once; a session that cannot be
// refreshed, or a service that needs Advanced Data Protection approval again,
// is ErrSignInRequired.
func (c *Client) Call(ctx context.Context, method, url string, body, out any) error {
	for attempt := 0; ; attempt++ {
		r, err := c.send(ctx, method, url, c.webHeaders(), body)
		if err != nil {
			return err
		}
		switch {
		case r.status == http.StatusOK:
			return decode(endpointName(url), r, out)
		case r.status == http.StatusLocked:
			return fmt.Errorf("%w: Advanced Data Protection approval expired", ErrSignInRequired)
		case (r.status == http.StatusUnauthorized || r.status == http.StatusMisdirectedRequest) && attempt == 0:
			if err := c.Refresh(ctx); err != nil {
				return err
			}
		default:
			return statusError(endpointName(url), r)
		}
	}
}
