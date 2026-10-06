package icloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAppleID  = "Owner@Example.com"
	testPassword = "correct horse"
	testCode     = "123456"
)

// fakeApple plays Apple's sign-in and setup services for one account, checking
// the client's SRP proofs with an RFC 5054 server and every token it echoes.
type fakeApple struct {
	t   *testing.T
	srv *httptest.Server

	mu                       sync.Mutex
	salt                     []byte
	iterations               int
	srp                      map[string]*srpServer // by challenge "c"
	clientA                  map[string][]byte
	sessionToken, trustToken string

	// Behaviour switches, set by tests while the server runs.
	escrow             atomic.Bool // 409s carry X-Apple-EDP
	phonesHTML         atomic.Bool // the auth state comes as an HTML page
	pcs                atomic.Bool // iCloud Drive needs Advanced Data Protection approval
	accountLoginStatus atomic.Int32
	// Observations.
	trustedRelogin           atomic.Bool
	escrowed, pushed, texted atomic.Int32
}

func newFakeApple(t *testing.T) *fakeApple {
	t.Helper()
	f := &fakeApple{t: t, salt: []byte("0123456789abcdef"), iterations: 1000, srp: map[string]*srpServer{}, clientA: map[string][]byte{}}
	f.accountLoginStatus.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /appleauth/auth/signin/init", f.srpInit)
	mux.HandleFunc("POST /appleauth/auth/escrow/init", f.srpInit)
	mux.HandleFunc("POST /appleauth/auth/signin/complete", f.signinComplete)
	mux.HandleFunc("POST /appleauth/auth/escrow/complete", f.escrowComplete)
	mux.HandleFunc("GET /appleauth/auth", f.authState)
	mux.HandleFunc("PUT /appleauth/auth/verify/trusteddevice/securitycode", func(w http.ResponseWriter, _ *http.Request) {
		f.pushed.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("PUT /appleauth/auth/verify/phone", func(w http.ResponseWriter, _ *http.Request) {
		f.texted.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /appleauth/auth/verify/trusteddevice/securitycode", f.code)
	mux.HandleFunc("POST /appleauth/auth/verify/phone/securitycode", f.code)
	mux.HandleFunc("GET /appleauth/auth/2sv/trust", f.trust)
	mux.HandleFunc("POST /setup/ws/1/accountLogin", f.accountLogin)
	mux.HandleFunc("POST /setup/ws/1/validate", f.validate)
	mux.HandleFunc("POST /setup/ws/1/requestPCS", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: pcsDriveCookie, Value: "pcs", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writeJSON(t, w, map[string]string{"status": "success"})
	})
	mux.HandleFunc("POST /drive/retrieveItemDetailsInFolders", f.drive)
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeApple) client(t *testing.T, s Session) *Client {
	t.Helper()
	c, err := New(Endpoints{Auth: f.srv.URL + "/appleauth/auth", Setup: f.srv.URL + "/setup/ws/1", Home: f.srv.URL}, s, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = f.srv.Client().Transport
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, v); err != nil {
		t.Error(err)
	}
}

func readJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Errorf("%s: %v", r.URL.Path, err)
	}
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fakeApple) srpInit(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Apple-Widget-Key") != clientID || r.Header.Get("X-Apple-Frame-Id") == "" {
		f.t.Errorf("%s: missing sign-in headers", r.URL.Path)
	}
	var req struct {
		A           string   `json:"a"`
		AccountName string   `json:"accountName"`
		Protocols   []string `json:"protocols"`
	}
	readJSON(f.t, r, &req)
	key, err := derivePassword(testPassword, f.salt, f.iterations, protocolS2KFO)
	if err != nil {
		f.t.Fatal(err)
	}
	server := newSRPServer(f.t, key)
	server.salt = f.salt
	// newSRPServer drew its own salt; recompute the verifier for ours.
	inner := hash([]byte(":"), key)
	server.v = new(big.Int).Exp(srpG, new(big.Int).SetBytes(hash(f.salt, inner)), srpN)
	B := new(big.Int).Add(new(big.Int).Mul(srpK, server.v), new(big.Int).Exp(srpG, server.b, srpN))
	server.B = B.Mod(B, srpN).Bytes()

	f.mu.Lock()
	c := base64.StdEncoding.EncodeToString(server.B[:8])
	f.srp[c], f.clientA[c] = server, b64(f.t, req.A)
	f.mu.Unlock()
	w.Header().Set("scnt", "scnt-1")
	writeJSON(f.t, w, map[string]any{
		"iteration": f.iterations, "salt": base64.StdEncoding.EncodeToString(f.salt), "protocol": protocolS2KFO,
		"b": base64.StdEncoding.EncodeToString(server.B), "c": c,
	})
}

// verify checks the client's M1 for challenge c and returns the session key.
func (f *fakeApple) verify(c, accountName, m1 string) ([]byte, bool) {
	f.mu.Lock()
	server, A := f.srp[c], f.clientA[c]
	f.mu.Unlock()
	if server == nil {
		return nil, false
	}
	K := server.key(A)
	hN, hG := hash(srpN.Bytes()), hash(pad(srpG))
	xor := make([]byte, len(hN))
	for i := range hN {
		xor[i] = hN[i] ^ hG[i]
	}
	want := hash(xor, hash([]byte(strings.ToLower(accountName))), f.salt, pad(new(big.Int).SetBytes(A)), server.B, K)
	return K, bytes.Equal(want, b64(f.t, m1))
}

func (f *fakeApple) issue(w http.ResponseWriter, token string) {
	f.mu.Lock()
	f.sessionToken = token
	f.mu.Unlock()
	w.Header().Set("X-Apple-Session-Token", token)
	w.Header().Set("X-Apple-ID-Session-Id", "sid-1")
	w.Header().Set("X-Apple-ID-Account-Country", "GRC")
}

func (f *fakeApple) signinComplete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("scnt") != "scnt-1" {
		f.t.Error("signin/complete without the scnt from signin/init")
	}
	var req struct {
		AccountName string   `json:"accountName"`
		M1          string   `json:"m1"`
		C           string   `json:"c"`
		TrustTokens []string `json:"trustTokens"`
	}
	readJSON(f.t, r, &req)
	if _, ok := f.verify(req.C, req.AccountName, req.M1); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.issue(w, "session-after-signin")
	f.mu.Lock()
	trusted := f.trustToken != "" && len(req.TrustTokens) == 1 && req.TrustTokens[0] == f.trustToken
	f.mu.Unlock()
	escrow := f.escrow.Load()
	f.trustedRelogin.Store(trusted)
	if trusted && !escrow {
		w.WriteHeader(http.StatusOK)
		return
	}
	if escrow {
		w.Header().Set("X-Apple-EDP", "1")
	}
	if trusted {
		w.WriteHeader(http.StatusConflict)
		return
	}
	// A code is needed: a 409 the escrow header must not short-cut.
	w.Header().Del("X-Apple-EDP")
	w.WriteHeader(http.StatusConflict)
}

func (f *fakeApple) escrowComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		M1 string `json:"m1"`
		C  string `json:"c"`
		K  string `json:"k"`
	}
	readJSON(f.t, r, &req)
	K, ok := f.verify(req.C, "", req.M1)
	if !ok || !bytes.Equal(K, b64(f.t, req.K)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.escrowed.Add(1)
	f.issue(w, "session-after-escrow")
	w.WriteHeader(http.StatusOK)
}

func (f *fakeApple) authState(w http.ResponseWriter, _ *http.Request) {
	phones := map[string]any{"trustedPhoneNumbers": []map[string]any{{"id": 2, "numberWithDialCode": "+30 •••• ••••12", "nonFTEU": true}}}
	if f.phonesHTML.Load() {
		boot, err := json.Marshal(map[string]any{"direct": map[string]any{"twoSV": map[string]any{"bridgeInitiateData": map[string]any{"phoneNumberVerification": phones}}}})
		if err != nil {
			f.t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><script type="application/json" class="boot_args">`+string(boot)+`</script></html>`)
		return
	}
	writeJSON(f.t, w, map[string]any{"phoneNumberVerification": phones})
}

func (f *fakeApple) code(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecurityCode struct {
			Code string `json:"code"`
		} `json:"securityCode"`
	}
	readJSON(f.t, r, &req)
	if r.Header.Get("X-Apple-ID-Session-Id") != "sid-1" {
		f.t.Error("code submitted without the session id")
	}
	if req.SecurityCode.Code != testCode {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Apple's answer since iOS 26.4: 409, valid, a new session token.
	f.issue(w, "session-after-code")
	if f.escrow.Load() {
		w.Header().Set("X-Apple-EDP", "1")
	}
	w.WriteHeader(http.StatusConflict)
	writeJSON(f.t, w, map[string]any{"securityCode": map[string]any{"code": req.SecurityCode.Code, "valid": true}})
}

func (f *fakeApple) trust(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.trustToken = "trust-1"
	f.mu.Unlock()
	w.Header().Set("X-Apple-TwoSV-Trust-Token", "trust-1")
	f.issue(w, "session-after-trust")
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeApple) services() map[string]any {
	return map[string]any{
		ServiceDrive: map[string]any{"url": f.srv.URL + "/drive", "pcsRequired": f.pcs.Load()},
		ServiceDocs:  map[string]any{"url": f.srv.URL + "/docs"},
	}
}

func (f *fakeApple) accountLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"dsWebAuthToken"`
		Trust string `json:"trustToken"`
	}
	readJSON(f.t, r, &req)
	f.mu.Lock()
	ok := req.Token == f.sessionToken && req.Trust == f.trustToken
	f.mu.Unlock()
	status := int(f.accountLoginStatus.Load())
	if !ok {
		f.t.Errorf("accountLogin with token %q trust %q", req.Token, req.Trust)
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "web", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(f.t, w, map[string]any{"dsInfo": map[string]any{"hsaVersion": 2}, "hsaChallengeRequired": false, "webservices": f.services()})
}

func (f *fakeApple) validate(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("X-APPLE-WEBAUTH-TOKEN"); err != nil || c.Value != "web" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(f.t, w, map[string]any{"dsInfo": map[string]any{"hsaVersion": 2}, "webservices": f.services()})
}

func (f *fakeApple) drive(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("X-APPLE-WEBAUTH-TOKEN"); err != nil || c.Value != "web" {
		w.WriteHeader(http.StatusMisdirectedRequest) // the web session expired
		return
	}
	writeJSON(f.t, w, []map[string]any{{"drivewsid": "FOLDER::com.apple.CloudDocs::root"}})
}

// login is the test account's sign-in request.
func login(awaiting func()) LoginRequest {
	return LoginRequest{AppleID: testAppleID, Password: []byte(testPassword), AwaitingApproval: awaiting}
}

// signIn runs the device-code sign-in end to end.
func signIn(t *testing.T, c *Client) {
	t.Helper()
	ctx := context.Background()
	l, needsCode, err := c.StartLogin(ctx, login(nil))
	if err != nil || !needsCode {
		t.Fatalf("StartLogin = needsCode %v, %v; want a code", needsCode, err)
	}
	if err := l.SubmitCode(ctx, testCode, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSignInWithACodeAndEscrow(t *testing.T) {
	f := newFakeApple(t)
	f.escrow.Store(true)
	c := f.client(t, Session{})
	signIn(t, c)

	s := c.Session()
	if s.TrustToken != "trust-1" || s.SessionToken != "session-after-trust" || s.AccountCountry != "GRC" ||
		s.Services[ServiceDrive].URL != f.srv.URL+"/drive" || s.SignedInAt.IsZero() || len(s.Cookies) == 0 {
		t.Fatalf("session = %+v", s)
	}
	if f.pushed.Load() != 1 || f.escrowed.Load() != 1 {
		t.Fatalf("pushed %d codes, %d escrow proofs; want 1 and 1", f.pushed.Load(), f.escrowed.Load())
	}
	saved, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte(testPassword)) {
		t.Fatal("the saved session contains the password")
	}
}

func TestSignInWithATrustTokenSkipsTheCode(t *testing.T) {
	f := newFakeApple(t)
	signIn(t, f.client(t, Session{}))

	// A later sign-in presents the trust token; Apple then wants only the
	// escrow proof.
	f.escrow.Store(true)
	c := f.client(t, Session{TrustToken: "trust-1"})
	_, needsCode, err := c.StartLogin(context.Background(), login(nil))
	if err != nil || needsCode || !f.trustedRelogin.Load() || f.escrowed.Load() != 1 || f.pushed.Load() != 1 {
		t.Fatalf("trusted sign-in: needsCode=%v err=%v escrowed=%d pushed=%d", needsCode, err, f.escrowed.Load(), f.pushed.Load())
	}
}

func TestSignInRejectsAWrongPasswordOrCode(t *testing.T) {
	f := newFakeApple(t)
	if _, _, err := f.client(t, Session{}).StartLogin(context.Background(), LoginRequest{AppleID: testAppleID, Password: []byte("wrong")}); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	l, _, err := f.client(t, Session{}).StartLogin(context.Background(), login(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SubmitCode(context.Background(), "000000", nil); !errors.Is(err, ErrCodeRejected) {
		t.Fatalf("wrong code: %v", err)
	}
}

func TestSignInByTextReadsPhonesFromTheSignInPage(t *testing.T) {
	f := newFakeApple(t)
	f.phonesHTML.Store(true)
	ctx := context.Background()
	l, _, err := f.client(t, Session{}).StartLogin(ctx, login(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Phones) != 1 || l.Phones[0].ID != 2 || l.Phones[0].NonFTEU == nil {
		t.Fatalf("phones = %+v", l.Phones)
	}
	if err := l.SendText(ctx, l.Phones[0]); err != nil || f.texted.Load() != 1 {
		t.Fatalf("SendText: %v (texted %d)", err, f.texted.Load())
	}
	if err := l.SubmitCode(ctx, testCode, &l.Phones[0]); err != nil {
		t.Fatal(err)
	}
}

func TestAdvancedDataProtectionApproval(t *testing.T) {
	f := newFakeApple(t)
	f.pcs.Store(true)
	c := f.client(t, Session{})
	ctx := context.Background()
	asked := false
	l, _, err := c.StartLogin(ctx, login(func() { asked = true }))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SubmitCode(ctx, testCode, nil); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("the owner was not told to approve access on a device")
	}
	if !c.NeedsDeviceApproval() || !c.hasCookie(f.srv.URL+"/drive", pcsDriveCookie) {
		t.Fatal("iCloud Drive's Advanced Data Protection cookie was not obtained")
	}
}

func TestCallRefreshesAnExpiredSessionOnce(t *testing.T) {
	f := newFakeApple(t)
	c := f.client(t, Session{})
	signIn(t, c)
	ctx := context.Background()
	drive, err := c.Service(ServiceDrive)
	if err != nil {
		t.Fatal(err)
	}

	// The saved session still works without the password.
	restored := f.client(t, c.Session())
	var items []map[string]any
	if err := restored.Call(ctx, http.MethodPost, drive.URL+"/retrieveItemDetailsInFolders", []any{}, &items); err != nil || len(items) != 1 {
		t.Fatalf("Call with the saved session: %v, %v", items, err)
	}

	// Without its web cookie the call fails with 421, the client exchanges the
	// session token again (validate fails first) and retries.
	stale := c.Session()
	stale.Cookies = nil
	again := f.client(t, stale)
	if err := again.Call(ctx, http.MethodPost, drive.URL+"/retrieveItemDetailsInFolders", []any{}, &items); err != nil {
		t.Fatalf("Call after the web session expired: %v", err)
	}

	// Once Apple refuses the tokens, the owner must sign in again.
	f.accountLoginStatus.Store(http.StatusMisdirectedRequest)
	stale2 := f.client(t, stale)
	if err := stale2.Call(ctx, http.MethodPost, drive.URL+"/retrieveItemDetailsInFolders", []any{}, &items); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("Call with refused tokens: %v; want ErrSignInRequired", err)
	}
}

func TestServiceBeforeSignIn(t *testing.T) {
	c, err := New(AppleEndpoints, Session{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Service(ServiceDrive); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("Service before sign-in: %v", err)
	}
}

func TestSessionSaveAndLoad(t *testing.T) {
	path := t.TempDir() + "/state/" + sessionFile
	if s, err := LoadSession(path); err != nil || s.SessionToken != "" {
		t.Fatalf("LoadSession before sign-in = %+v, %v", s, err)
	}
	f := newFakeApple(t)
	c := f.client(t, Session{})
	signIn(t, c)
	if err := SaveSession(path, c.Session()); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSession(path)
	if err != nil || s.TrustToken != "trust-1" || len(s.Cookies) == 0 || s.Services[ServiceDrive].URL == "" {
		t.Fatalf("loaded session = %+v, %v", s, err)
	}
}

func TestSessionFileNeverOverwritesANewerSignIn(t *testing.T) {
	path := t.TempDir() + "/" + sessionFile
	if err := SaveSession(path, Session{TrustToken: "old"}); err != nil {
		t.Fatal(err)
	}
	f, s, err := OpenSessionFile(path)
	if err != nil || s.TrustToken != "old" {
		t.Fatalf("OpenSessionFile = %+v, %v", s, err)
	}
	if saved, err := f.Save(Session{TrustToken: "refreshed"}); err != nil || !saved {
		t.Fatalf("Save of an unchanged file: %v, %v", saved, err)
	}
	// The owner signs in again while the agent holds the session.
	if err := SaveSession(path, Session{TrustToken: "new sign-in"}); err != nil {
		t.Fatal(err)
	}
	if saved, err := f.Save(Session{TrustToken: "stale"}); err != nil || saved {
		t.Fatalf("Save over a newer sign-in: %v, %v", saved, err)
	}
	if s, err := LoadSession(path); err != nil || s.TrustToken != "new sign-in" {
		t.Fatalf("session after the race = %+v, %v", s, err)
	}
}

func TestSignInTellsARefusedAccountFromAWrongPassword(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Endpoints{Auth: srv.URL, Setup: srv.URL, Home: srv.URL}, Session{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = srv.Client().Transport
	if _, _, err := c.StartLogin(context.Background(), login(nil)); !errors.Is(err, ErrAccountRefused) {
		t.Fatalf("403: %v; want ErrAccountRefused", err)
	}
}

func TestLoginSessionStartsFresh(t *testing.T) {
	path := t.TempDir() + "/" + sessionFile
	if err := SaveSession(path, Session{
		AppleID: "Owner@Example.com", TrustToken: "trust", SessionToken: "old",
		Services: map[string]Service{ServiceDrive: {URL: "https://old"}},
	}); err != nil {
		t.Fatal(err)
	}
	// The same account, in another case, keeps only its trust token.
	if s := LoginSession(path, "owner@example.com"); s.TrustToken != "trust" || s.SessionToken != "" || s.Services != nil || len(s.Cookies) != 0 {
		t.Fatalf("same account: %+v", s)
	}
	// Another account keeps nothing.
	if s := LoginSession(path, "other@example.com"); s.TrustToken != "" {
		t.Fatalf("other account kept the trust token: %+v", s)
	}
	// An unreadable saved session never blocks signing in again.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := LoginSession(path, "owner@example.com"); s.AppleID != "owner@example.com" || s.TrustToken != "" {
		t.Fatalf("corrupt file: %+v", s)
	}
}

func TestApprovalPromptOnTheTrustTokenPath(t *testing.T) {
	f := newFakeApple(t)
	signIn(t, f.client(t, Session{}))
	f.escrow.Store(true)
	f.pcs.Store(true)
	asked := false
	c := f.client(t, Session{TrustToken: "trust-1"})
	if _, needsCode, err := c.StartLogin(context.Background(), login(func() { asked = true })); err != nil || needsCode {
		t.Fatalf("trusted sign-in: needsCode=%v err=%v", needsCode, err)
	}
	if !asked {
		t.Fatal("the owner was not told to approve access on a device")
	}
}

func TestCallsNeverFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Endpoints{Auth: srv.URL, Setup: srv.URL, Home: srv.URL}, Session{Services: map[string]Service{ServiceDrive: {URL: srv.URL}}}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.Call(context.Background(), http.MethodPost, srv.URL+"/drive", map[string]string{"token": "secret"}, &out); err == nil || followed.Load() {
		t.Fatalf("redirected call: err=%v followed=%v", err, followed.Load())
	}
}

func TestOversizedResponseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte(" "), maxBody+1))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Endpoints{Auth: srv.URL, Setup: srv.URL, Home: srv.URL}, Session{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.send(context.Background(), http.MethodGet, srv.URL, c.webHeaders(), nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized response: %v", err)
	}
}

func TestTrustedPhonesKnowsEmptyFromUnknown(t *testing.T) {
	for body, wantErr := range map[string]bool{
		`{"trustedPhoneNumbers":[]}`:                                     false,
		`{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":1}]}}`: false,
		`{"somethingElse":true}`:                                         true,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		c, err := New(Endpoints{Auth: srv.URL, Setup: srv.URL, Home: srv.URL}, Session{}, time.Minute, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		l := &Login{c: c}
		_, err = l.trustedPhones(context.Background())
		srv.Close()
		if (err != nil) != wantErr {
			t.Errorf("%s: err=%v, want error %v", body, err, wantErr)
		}
	}
}
