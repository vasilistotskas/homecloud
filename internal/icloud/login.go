package icloud

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Sign-in errors the owner can act on.
var (
	// ErrBadCredentials: Apple rejected the Apple Account or password.
	ErrBadCredentials = errors.New("sign-in rejected: wrong Apple Account or password")
	// ErrCodeRejected: the two-factor code was wrong or expired.
	ErrCodeRejected = errors.New("verification code rejected")
)

const (
	// pcsPollInterval and pcsPollAttempts bound the wait for the owner to
	// approve Advanced Data Protection access on a trusted device (5 minutes).
	pcsPollInterval = 10 * time.Second
	pcsPollAttempts = 30
	// pcsDriveApp and pcsDriveCookie are iCloud Drive's Advanced Data
	// Protection app name and cookie (rclone's iclouddrive backend).
	pcsDriveApp    = "iclouddrive"
	pcsDriveCookie = "X-APPLE-WEBAUTH-PCS-Documents"
)

// clientInfo is the X-Apple-I-FD-Client-Info header of Apple's web client.
var clientInfo = func() string {
	b, err := json.Marshal(map[string]string{"U": userAgent, "L": "en-US", "Z": "GMT+00:00", "V": "1.1", "F": ""})
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// Phone is a trusted phone number that can receive a code by text.
type Phone struct {
	ID int `json:"id"`
	// Number is masked by Apple, e.g. "+30 •••• ••••12".
	Number  string `json:"numberWithDialCode"`
	NonFTEU *bool  `json:"nonFTEU,omitempty"`
}

// Login is one interactive sign-in. It holds the password in memory until the
// sign-in completes, because Apple may ask for a second password proof.
type Login struct {
	c        *Client
	appleID  string
	password string
	frameID  string
	// Apple's per-flow values, echoed back on every sign-in request.
	scnt, sessionID, authAttributes string
	// Phones is filled when a code is needed.
	Phones []Phone
	// AwaitingApproval, when set, is called once the account turns out to use
	// Advanced Data Protection and iCloud Drive access waits for the owner to
	// approve it on a trusted device.
	AwaitingApproval func()
}

// StartLogin signs in with the password. It returns needsCode when Apple wants a
// two-factor code; a code has then been pushed to the trusted devices, and
// Phones lists the numbers that can get one by text instead.
func (c *Client) StartLogin(ctx context.Context, appleID, password string) (l *Login, needsCode bool, err error) {
	frame := make([]byte, 16)
	if _, randErr := rand.Read(frame); randErr != nil {
		return nil, false, fmt.Errorf("frame id: %w", randErr)
	}
	l = &Login{c: c, appleID: appleID, password: password, frameID: fmt.Sprintf("%x", frame)}
	c.mu.Lock()
	c.session.AppleID = appleID
	trust := c.session.TrustToken
	c.mu.Unlock()

	srp, init, err := l.srpInit(ctx, "/signin/init", appleID)
	if err != nil {
		return nil, false, err
	}
	m1, m2, err := l.prove(srp, init, appleID)
	if err != nil {
		return nil, false, err
	}
	trustTokens := []string{}
	if trust != "" {
		trustTokens = []string{trust}
	}
	r, err := l.post(ctx, "/signin/complete?isRememberMeEnabled=true", map[string]any{
		"accountName": appleID, "m1": m1, "m2": m2, "c": init.C, "trustTokens": trustTokens,
	})
	if err != nil {
		return nil, false, err
	}
	switch r.status {
	case http.StatusOK:
		l.takeSession(r)
		return l, false, l.finish(ctx, false)
	case http.StatusConflict:
		l.takeSession(r)
		if requiresEscrow(r) {
			// A valid trust token: no code, but Apple wants the escrow proof.
			if escrowErr := l.escrow(ctx); escrowErr != nil {
				return nil, false, escrowErr
			}
			return l, false, l.finish(ctx, false)
		}
		if l.Phones, err = l.trustedPhones(ctx); err != nil {
			return nil, false, err
		}
		// Since iOS 26.4 the code is no longer pushed by the sign-in itself.
		if err = l.expect(ctx, http.MethodPut, "/verify/trusteddevice/securitycode", nil, http.StatusOK, http.StatusAccepted); err != nil {
			return nil, false, fmt.Errorf("request a code on the trusted devices: %w", err)
		}
		return l, true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, false, ErrBadCredentials
	default:
		return nil, false, statusError("sign-in", r)
	}
}

// SendText asks Apple to text a code to a trusted phone number.
func (l *Login) SendText(ctx context.Context, p Phone) error {
	return l.expect(ctx, http.MethodPut, "/verify/phone",
		map[string]any{"phoneNumber": map[string]any{"id": p.ID}, "mode": "sms"}, http.StatusOK)
}

// SubmitCode completes the sign-in with a code from a trusted device, or from
// the text sent to phone when phone is not nil. On success the session is
// trusted and the web session established.
func (l *Login) SubmitCode(ctx context.Context, code string, phone *Phone) error {
	path := "/verify/trusteddevice/securitycode"
	body := map[string]any{"securityCode": map[string]string{"code": code}}
	accepted := []int{http.StatusNoContent}
	if phone != nil {
		path = "/verify/phone/securitycode"
		number := map[string]any{"id": phone.ID}
		if phone.NonFTEU != nil {
			number["nonFTEU"] = *phone.NonFTEU
		}
		body["phoneNumber"], body["mode"] = number, "sms"
		accepted = []int{http.StatusOK}
	}
	r, err := l.post(ctx, path, body)
	if err != nil {
		return err
	}
	switch {
	case slices.Contains(accepted, r.status):
		l.takeSession(r)
	case r.status == http.StatusConflict:
		// Since iOS 26.4 a valid code may be answered with 409: valid when the
		// body says so or a new session token is issued.
		var d struct {
			SecurityCode struct {
				Valid bool `json:"valid"`
			} `json:"securityCode"`
		}
		_ = json.Unmarshal(r.body, &d) //nolint:errcheck // a non-JSON body means "not valid"
		if !d.SecurityCode.Valid && r.header.Get("X-Apple-Session-Token") == "" {
			return ErrCodeRejected
		}
		l.takeSession(r)
		if requiresEscrow(r) {
			if err := l.escrow(ctx); err != nil {
				return err
			}
		}
	case r.status == http.StatusBadRequest:
		return ErrCodeRejected
	default:
		return statusError("verification code", r)
	}
	return l.finish(ctx, true)
}

// finish trusts this client when a code was used, then establishes the web
// session and, under Advanced Data Protection, iCloud Drive's approval.
func (l *Login) finish(ctx context.Context, trust bool) error {
	if trust {
		r, err := l.send(ctx, http.MethodGet, "/2sv/trust", nil)
		if err != nil {
			return err
		}
		if r.status != http.StatusNoContent && r.status != http.StatusOK {
			return statusError("trust this browser", r)
		}
		l.takeSession(r)
		token := r.header.Get("X-Apple-TwoSV-Trust-Token")
		if token == "" {
			return errors.New("trust this browser: Apple issued no trust token")
		}
		l.c.mu.Lock()
		l.c.session.TrustToken = token
		l.c.mu.Unlock()
	}
	if err := l.c.accountLogin(ctx); err != nil {
		return err
	}
	if err := l.c.approveDrive(ctx, l.AwaitingApproval); err != nil {
		return err
	}
	l.c.mu.Lock()
	l.c.session.SignedInAt = l.c.now()
	l.c.mu.Unlock()
	l.password = ""
	return nil
}

// approveDrive obtains iCloud Drive's cookie under Advanced Data Protection,
// which the owner approves on a trusted device. Without ADP it does nothing.
func (c *Client) approveDrive(ctx context.Context, awaiting func()) error {
	svc, err := c.Service(ServiceDrive)
	if err != nil {
		return err
	}
	if !svc.PCSRequired || c.hasCookie(svc.URL, pcsDriveCookie) {
		return nil
	}
	if awaiting != nil {
		awaiting()
	}
	for range pcsPollAttempts {
		r, err := c.send(ctx, http.MethodPost, c.ep.Setup+"/requestPCS", c.webHeaders(),
			map[string]any{"appName": pcsDriveApp, "derivedFromUserAction": true})
		if err != nil {
			return err
		}
		if r.status != http.StatusOK {
			return statusError("Advanced Data Protection approval", r)
		}
		var d struct {
			Status string `json:"status"`
		}
		if err := decode("requestPCS", r, &d); err != nil {
			return err
		}
		if d.Status == "success" {
			if !c.hasCookie(svc.URL, pcsDriveCookie) {
				return errors.New("iCloud Drive access was approved but its cookie is missing")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for Advanced Data Protection approval: %w", ctx.Err())
		case <-time.After(pcsPollInterval):
		}
	}
	return errors.New("iCloud Drive access under Advanced Data Protection was not approved within 5 minutes")
}

// NeedsDeviceApproval reports whether the account uses Advanced Data
// Protection, so the owner must approve iCloud Drive access on a device.
func (c *Client) NeedsDeviceApproval() bool {
	svc, err := c.Service(ServiceDrive)
	return err == nil && svc.PCSRequired
}

func (c *Client) hasCookie(rawURL, name string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(c.jar.Cookies(u), func(k *http.Cookie) bool { return k.Name == name })
}

// srpInitResponse is the server's SRP challenge.
type srpInitResponse struct {
	Iteration int    `json:"iteration"`
	Salt      string `json:"salt"`
	Protocol  string `json:"protocol"`
	B         string `json:"b"`
	C         string `json:"c"`
}

func (l *Login) srpInit(ctx context.Context, path, accountName string) (*srpClient, srpInitResponse, error) {
	srp, err := newSRPClient()
	if err != nil {
		return nil, srpInitResponse{}, err
	}
	r, err := l.post(ctx, path, map[string]any{
		"a": base64.StdEncoding.EncodeToString(srp.publicValue()), "accountName": accountName,
		"protocols": []string{protocolS2K, protocolS2KFO},
	})
	if err != nil {
		return nil, srpInitResponse{}, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, srpInitResponse{}, ErrBadCredentials
	default:
		return nil, srpInitResponse{}, statusError(path, r)
	}
	var init srpInitResponse
	if err := decode(path, r, &init); err != nil {
		return nil, srpInitResponse{}, err
	}
	if init.Iteration <= 0 || init.Salt == "" || init.B == "" || init.C == "" {
		return nil, srpInitResponse{}, fmt.Errorf("%s: incomplete SRP challenge", path)
	}
	l.takeFlow(r)
	return srp, init, nil
}

// prove derives the password key and returns the base64 proofs M1 and M2.
func (l *Login) prove(srp *srpClient, init srpInitResponse, accountName string) (m1, m2 string, err error) {
	salt, err := base64.StdEncoding.DecodeString(init.Salt)
	if err != nil {
		return "", "", fmt.Errorf("decode SRP salt: %w", err)
	}
	B, err := base64.StdEncoding.DecodeString(init.B)
	if err != nil {
		return "", "", fmt.Errorf("decode SRP server value: %w", err)
	}
	key, err := derivePassword(l.password, salt, init.Iteration, init.Protocol)
	if err != nil {
		return "", "", err
	}
	if err := srp.prove(accountName, key, salt, B); err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(srp.M1), base64.StdEncoding.EncodeToString(srp.M2), nil
}

// escrow is Apple's second password proof (since iOS 26.4), asked for with
// X-Apple-EDP or X-Apple-PDP on a 409: SRP again with an empty account name,
// returning the session key K.
func (l *Login) escrow(ctx context.Context) error {
	srp, init, err := l.srpInit(ctx, "/escrow/init", "")
	if err != nil {
		return fmt.Errorf("escrow: %w", err)
	}
	m1, m2, err := l.prove(srp, init, "")
	if err != nil {
		return fmt.Errorf("escrow: %w", err)
	}
	r, err := l.post(ctx, "/escrow/complete", map[string]any{
		"m1": m1, "m2": m2, "c": init.C, "k": base64.StdEncoding.EncodeToString(srp.K),
	})
	if err != nil {
		return fmt.Errorf("escrow: %w", err)
	}
	if r.status != http.StatusOK && r.status != http.StatusNoContent {
		return statusError("escrow", r)
	}
	l.takeSession(r)
	return nil
}

func requiresEscrow(r response) bool {
	return r.header.Get("X-Apple-EDP") != "" || r.header.Get("X-Apple-PDP") != ""
}

// trustedPhones reads the numbers that can receive a code. Apple answers with
// JSON, with the numbers at the top level or nested, or with an HTML page that
// carries the same data in its boot_args script.
func (l *Login) trustedPhones(ctx context.Context) ([]Phone, error) {
	r, err := l.send(ctx, http.MethodGet, "", nil)
	if err != nil {
		return nil, err
	}
	if r.status != http.StatusOK {
		return nil, statusError("trusted phone numbers", r)
	}
	type phones struct {
		TrustedPhoneNumbers []Phone `json:"trustedPhoneNumbers"`
	}
	var d struct {
		phones
		PhoneNumberVerification *phones `json:"phoneNumberVerification"`
		BridgeInitiateData      *struct {
			PhoneNumberVerification *phones `json:"phoneNumberVerification"`
		} `json:"bridgeInitiateData"`
	}
	data := r.body
	if m := bootArgs.FindSubmatch(data); m != nil {
		var boot struct {
			Direct struct {
				TwoSV jsontext.Value `json:"twoSV"`
			} `json:"direct"`
		}
		if err := json.Unmarshal(m[1], &boot); err != nil {
			return nil, fmt.Errorf("decode sign-in page data: %w", err)
		}
		data = boot.Direct.TwoSV
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("decode trusted phone numbers: %w", err)
	}
	switch {
	case len(d.TrustedPhoneNumbers) > 0:
		return d.TrustedPhoneNumbers, nil
	case d.PhoneNumberVerification != nil:
		return d.PhoneNumberVerification.TrustedPhoneNumbers, nil
	case d.BridgeInitiateData != nil && d.BridgeInitiateData.PhoneNumberVerification != nil:
		return d.BridgeInitiateData.PhoneNumberVerification.TrustedPhoneNumbers, nil
	default:
		return nil, nil
	}
}

var bootArgs = regexp.MustCompile(`(?s)<script[^>]*class="boot_args"[^>]*>(.*?)</script>`)

// authHeaders are the sign-in service's headers for this flow.
func (l *Login) authHeaders() http.Header {
	h := http.Header{
		"Accept":                           {"application/json"},
		"Content-Type":                     {"application/json"},
		"User-Agent":                       {userAgent},
		"Origin":                           {strings.TrimSuffix(l.c.ep.Auth, "/appleauth/auth")},
		"Referer":                          {strings.TrimSuffix(l.c.ep.Auth, "/appleauth/auth") + "/"},
		"X-Apple-Widget-Key":               {clientID},
		"X-Apple-OAuth-Client-Id":          {clientID},
		"X-Apple-I-FD-Client-Info":         {clientInfo},
		"X-Apple-OAuth-Response-Type":      {"code"},
		"X-Apple-OAuth-Response-Mode":      {"web_message"},
		"X-Apple-OAuth-Client-Type":        {"firstPartyAuth"},
		"X-Apple-OAuth-Redirect-URI":       {l.c.ep.Home},
		"X-Apple-OAuth-Require-Grant-Code": {"true"},
		"X-Apple-Offer-Security-Upgrade":   {"1"},
		"X-Apple-Domain-Id":                {"3"},
		"X-Apple-Frame-Id":                 {l.frameID},
		"X-Apple-OAuth-State":              {l.frameID},
	}
	for k, v := range map[string]string{"scnt": l.scnt, "X-Apple-ID-Session-Id": l.sessionID, "X-Apple-Auth-Attributes": l.authAttributes} {
		if v != "" {
			h.Set(k, v)
		}
	}
	return h
}

func (l *Login) send(ctx context.Context, method, path string, body any) (response, error) {
	r, err := l.c.send(ctx, method, l.c.ep.Auth+path, l.authHeaders(), body)
	if err == nil {
		l.takeFlow(r)
	}
	return r, err
}

func (l *Login) post(ctx context.Context, path string, body any) (response, error) {
	return l.send(ctx, http.MethodPost, path, body)
}

// expect sends a request and accepts only the given statuses.
func (l *Login) expect(ctx context.Context, method, path string, body any, ok ...int) error {
	r, err := l.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	if !slices.Contains(ok, r.status) {
		return statusError(path, r)
	}
	return nil
}

// takeFlow keeps the per-flow values Apple returns on sign-in responses.
func (l *Login) takeFlow(r response) {
	if v := r.header.Get("scnt"); v != "" {
		l.scnt = v
	}
	if v := r.header.Get("X-Apple-Auth-Attributes"); v != "" {
		l.authAttributes = v
	}
}

// takeSession keeps the session values a sign-in step issues.
func (l *Login) takeSession(r response) {
	if v := r.header.Get("X-Apple-ID-Session-Id"); v != "" {
		l.sessionID = v
	}
	l.c.mu.Lock()
	defer l.c.mu.Unlock()
	if v := r.header.Get("X-Apple-Session-Token"); v != "" {
		l.c.session.SessionToken = v
		if l.sessionID == "" {
			// Apple's web client falls back to the session token.
			l.sessionID = v
		}
	}
	if v := r.header.Get("X-Apple-ID-Account-Country"); v != "" {
		l.c.session.AccountCountry = v
	}
}
