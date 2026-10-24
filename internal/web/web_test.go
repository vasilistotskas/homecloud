package web_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3/agentv3connect"
	"github.com/vasilistotskas/homecloud/internal/api"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/media"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
	"github.com/vasilistotskas/homecloud/internal/web"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fakeAgent struct {
	hub     *app.Hub
	started bool
	// archive is what CheckArchive reports.
	archive error
	// signedIn is when homecloud was signed in to iCloud.
	signedIn time.Time
}

func (f *fakeAgent) Hub() *app.Hub       { return f.hub }
func (f *fakeAgent) RequestCycle() bool  { return f.started }
func (f *fakeAgent) CheckArchive() error { return f.archive }

func (f *fakeAgent) ICloudSignedInAt() (time.Time, error) { return f.signedIn, nil }

// notes records the alerts sent, or fails each send with fail.
type notes struct {
	mu   sync.Mutex
	msgs []notify.Msg
	fail error
}

func (n *notes) Send(_ context.Context, m notify.Msg) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail != nil {
		return n.fail
	}
	n.msgs = append(n.msgs, m)
	return nil
}

func (n *notes) failWith(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fail = err
}

func (n *notes) sent() []notify.Msg {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.msgs)
}

type env struct {
	// browser is the host:port a browser on this computer uses.
	browser string
	// root is the archive's root.
	root     string
	notes    *notes
	renderer *thumbs.Renderer
	url      string
	host     string
	token    string
	db       *ledger.DB
	agent    *fakeAgent
	client   agentv3connect.AgentServiceClient
}

// setup starts the API over a fresh ledger; opts adjust the fake agent
// before the server starts.
func setup(t *testing.T, opts ...func(*fakeAgent)) *env {
	t.Helper()
	return setupWith(t, "", opts...)
}

// setupWith is setup with a public origin, as behind Tailscale Serve.
func setupWith(t *testing.T, publicOrigin string, opts ...func(*fakeAgent)) *env {
	t.Helper()
	ctx := context.Background()
	db, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	token, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	authn := auth.New(token, 7*24*time.Hour, publicOrigin != "", db, func() time.Time { return t0 })
	agent := &fakeAgent{hub: app.NewHub(), started: true}
	for _, opt := range opts {
		opt(agent)
	}
	signer, err := media.NewSigner(func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	includeHidden := true
	renderer := thumbs.New(config.FFmpeg{}, filepath.Join(t.TempDir(), "thumbs"), slog.New(slog.DiscardHandler))
	cfg := &config.Config{
		Storage:      config.Storage{Root: t.TempDir(), MinFreePercent: 15},
		ICloud:       config.ICloud{AppleID: "someone@example.com", IncludeHidden: &includeHidden},
		Web:          config.Web{Listen: listen, PublicOrigin: &publicOrigin},
		Schedule:     config.Schedule{SyncInterval: config.Duration(time.Hour)},
		SafeToDelete: config.SafeToDelete{MinAge: config.Duration(365 * 24 * time.Hour)},
	}

	discard := slog.New(slog.DiscardHandler)
	sent := &notes{}
	h, err := web.Handler(web.Options{
		Listen:       listen,
		PublicOrigin: publicOrigin,
		Service: api.New(api.Deps{
			Config: cfg, ConfigPath: "config.toml", AgentVersion: "test",
			Ledger: db, Agent: agent, Notifier: sent, Auth: authn, Media: signer, Log: discard,
			Now: func() time.Time { return t0 },
		}),
		App: fstest.MapFS{
			"index.html":        {Data: []byte(`<div id="root"></div>`)},
			"assets/index-1.js": {Data: []byte("console.log(1)")},
		},
		Media: media.Handler(signer, authn, db, renderer, discard),
		Auth:  authn,
		Log:   discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	browser, err := web.BrowserAddr(listen)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &env{
		url: srv.URL, host: listen, browser: browser, root: cfg.Storage.Root, notes: sent, token: token, db: db, agent: agent, renderer: renderer,
		client: agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL), auth.Bearer(token))),
	}
}

func code(err error) connect.Code {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code()
	}
	return connect.CodeUnknown
}

func seed(t *testing.T, db *ledger.DB, assetID string, quarantine bool) {
	t.Helper()
	ctx := context.Background()
	r := ledger.Rendition{
		Library: "PrimarySync", AssetID: assetID, Version: "original", Filename: assetID + ".MOV",
		Downloaded: true, LocalPath: "/archive/" + assetID + ".MOV", SizeBytes: 10,
		SHA256: strings.Repeat("a", 64), MediaType: "video", RequiresStructure: true, RequiresProbe: true,
		CreatedAt: t0, SeenAt: t0,
	}
	if _, err := db.Ingest(ctx, []ledger.Rendition{r}, t0); err != nil {
		t.Fatal(err)
	}
	if quarantine {
		if err := db.RecordMismatch(ctx, ledger.Key{Library: "PrimarySync", AssetID: assetID, Version: "original"},
			ledger.TierStructure, "no moov box", t0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequiresCredentials(t *testing.T) {
	e := setup(t)
	anon := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, e.url)))
	_, err := anon.GetInfo(context.Background(), &agentv3.GetInfoRequest{})
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous GetInfo = %v, want unauthenticated", err)
	}
	wrong := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, e.url), auth.Bearer("nope")))
	if _, wrongErr := wrong.GetInfo(context.Background(), &agentv3.GetInfoRequest{}); code(wrongErr) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token = %v, want unauthenticated", wrongErr)
	}
	info, err := e.client.GetInfo(context.Background(), &agentv3.GetInfoRequest{})
	if err != nil || info.GetApiVersion() != api.Version {
		t.Fatalf("GetInfo = %v, %v", info, err)
	}
	// The service's catch-all for unknown methods is behind the same check.
	if got := rawPost(t, e, "/"+agentv3connect.AgentServiceName+"/NoSuchMethod", nil); got.status != http.StatusUnauthorized {
		t.Fatalf("anonymous call to an unknown method = %d, want 401", got.status)
	}
}

type rawResult struct {
	status  int
	cookies []*http.Cookie
}

func rawPost(t *testing.T, e *env, procedure string, hdr map[string]string) rawResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.url+procedure, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := res.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return rawResult{status: res.StatusCode, cookies: res.Cookies()}
}

func TestHostAndOriginChecks(t *testing.T) {
	e := setup(t)
	bearer := "Bearer " + e.token
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"own host", map[string]string{"Authorization": bearer}, http.StatusOK},
		{"rebinding host", map[string]string{"Authorization": bearer, "Host": "evil.example:80"}, http.StatusForbidden},
		{"localhost alias", map[string]string{"Authorization": bearer, "Host": strings.Replace(e.host, "127.0.0.1", "localhost", 1)}, http.StatusForbidden},
		{"foreign origin", map[string]string{"Authorization": bearer, "Origin": "https://evil.example"}, http.StatusForbidden},
		{"desktop origin", map[string]string{"Authorization": bearer, "Origin": "http://tauri.localhost"}, http.StatusOK},
	}
	for _, c := range cases {
		res := rawPost(t, e, agentv3connect.AgentServiceGetInfoProcedure, c.hdr)
		if res.status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, res.status, c.want)
		}
	}
}

// Behind Tailscale Serve the agent sees the public host and origin, which it
// accepts in addition to its own; nothing else changes.
func TestPublicOrigin(t *testing.T) {
	const public = "https://nas.example.ts.net"
	e := setupWith(t, public)
	bearer := "Bearer " + e.token
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"public host and origin", map[string]string{"Authorization": bearer, "Host": "nas.example.ts.net", "Origin": public}, http.StatusOK},
		{"own host still", map[string]string{"Authorization": bearer}, http.StatusOK},
		{"public host over http", map[string]string{"Authorization": bearer, "Host": "nas.example.ts.net", "Origin": "http://nas.example.ts.net"}, http.StatusForbidden},
		{"another tailnet host", map[string]string{"Authorization": bearer, "Host": "other.example.ts.net"}, http.StatusForbidden},
	}
	for _, c := range cases {
		res := rawPost(t, e, agentv3connect.AgentServiceGetInfoProcedure, c.hdr)
		if res.status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, res.status, c.want)
		}
	}

	lc, err := e.client.CreateLoginCode(context.Background(), signInCode)
	if err != nil {
		t.Fatal(err)
	}
	if status, cookies := exchange(t, e, lc.GetCode(), "nas.example.ts.net", public); status != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("exchange over the public origin: status %d cookies %+v; want one Secure cookie", status, cookies)
	}
}

// A desktop app on another computer pairs once with a login code and then
// uses its own token, which can do what a browser session can (not mint
// login codes) until it is revoked.
func TestPairedDevice(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	signIn, err := e.client.CreateLoginCode(ctx, signInCode)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := e.client.CreateLoginCode(ctx, pairCode)
	if err != nil {
		t.Fatal(err)
	}
	// The desktop app pairs from its own origin, with no credential yet.
	anon := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, e.url), origin("http://tauri.localhost")))
	if _, err = anon.PairDevice(ctx, &agentv3.PairDeviceRequest{Code: lc.GetCode(), Name: " "}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("blank name = %v", err)
	}
	if _, err = anon.PairDevice(ctx, &agentv3.PairDeviceRequest{Code: signIn.GetCode(), Name: "Laptop"}); code(err) != connect.CodePermissionDenied {
		t.Fatalf("pairing with a sign-in code = %v", err)
	}
	paired, err := anon.PairDevice(ctx, &agentv3.PairDeviceRequest{Code: lc.GetCode(), Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if sent := e.notes.sent(); len(sent) != 1 || strings.Contains(sent[0].Body, "Laptop") {
		t.Fatalf("pairing alerts = %+v; want one, naming no device", sent)
	}
	if _, err = anon.PairDevice(ctx, &agentv3.PairDeviceRequest{Code: lc.GetCode(), Name: "Laptop"}); code(err) != connect.CodePermissionDenied {
		t.Fatalf("reused code = %v", err)
	}

	device := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, e.url),
		origin("http://tauri.localhost"), auth.Bearer(paired.GetToken())))
	if _, err = device.GetInfo(ctx, &agentv3.GetInfoRequest{}); err != nil {
		t.Fatalf("device call: %v", err)
	}
	if _, err = device.CreateLoginCode(ctx, signInCode); code(err) != connect.CodePermissionDenied {
		t.Fatalf("a device minted a login code: %v", err)
	}
	list, err := device.ListDevices(ctx, &agentv3.ListDevicesRequest{})
	if err != nil || len(list.GetDevices()) != 1 || list.GetDevices()[0].GetName() != "Laptop" ||
		list.GetDevices()[0].GetId() != paired.GetDeviceId() {
		t.Fatalf("devices = %v, %v", list, err)
	}

	// The device watches the overview; revoking it ends that stream too.
	stream, err := device.WatchOverview(ctx, &agentv3.WatchOverviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err = stream.Receive(); err != nil {
		t.Fatalf("first overview: %v", err)
	}

	if _, err = e.client.RevokeDevice(ctx, &agentv3.RevokeDeviceRequest{DeviceId: paired.GetDeviceId()}); err != nil {
		t.Fatal(err)
	}
	if _, err = device.GetInfo(ctx, &agentv3.GetInfoRequest{}); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked device call = %v, want Unauthenticated", err)
	}
	e.agent.hub.Changed()
	if _, err = stream.Receive(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("the revoked device's stream went on: %v", err)
	}
	if _, err = e.client.RevokeDevice(ctx, &agentv3.RevokeDeviceRequest{DeviceId: paired.GetDeviceId()}); code(err) != connect.CodeNotFound {
		t.Fatalf("revoking twice = %v", err)
	}
}

// origin sends requests from a browser origin, as the desktop app's webview.
func origin(o string) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			info, ok := connect.CallInfoForClientContext(ctx)
			if !ok {
				return nil, connect.NewError(connect.CodeInternal, "no call information to carry the origin")
			}
			info.RequestHeader().Set("Origin", o)
			return next(ctx, spec)
		}
	}
}

func TestDesktopPreflight(t *testing.T) {
	e := setup(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, e.url+agentv3connect.AgentServiceGetInfoProcedure, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://tauri.localhost")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	// As WebView2 sends it: lowercase, sorted, comma-separated (Fetch standard).
	req.Header.Set("Access-Control-Request-Headers", "authorization,connect-protocol-version,content-type")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
		t.Fatalf("allow-origin = %q", got)
	}
	if got := strings.ToLower(res.Header.Get("Access-Control-Allow-Headers")); !strings.Contains(got, "authorization") {
		t.Fatalf("allow-headers = %q, want authorization", got)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", res.StatusCode)
	}
}

func TestBrowserLogin(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	lc, err := e.client.CreateLoginCode(ctx, signInCode)
	if err != nil {
		t.Fatal(err)
	}
	self := "http://" + e.browser

	bad := rawPost(t, e, agentv3connect.AgentServiceExchangeLoginCodeProcedure, map[string]string{"Host": e.browser, "Origin": self})
	if bad.status == http.StatusOK || len(bad.cookies) != 0 {
		t.Fatalf("empty code: status %d cookies %v", bad.status, bad.cookies)
	}
	// At the listen address a cookie would reach every service on 127.0.0.1.
	if status, cookies := exchange(t, e, lc.GetCode(), e.host, "http://"+e.host); status == http.StatusOK || len(cookies) != 0 {
		t.Fatalf("exchange at the listen address: status %d cookies %v", status, cookies)
	}

	status, cookies := exchange(t, e, lc.GetCode(), e.browser, self)
	if status != http.StatusOK || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("exchange: status %d cookies %+v", status, cookies)
	}
	session := cookies[0].Name + "=" + cookies[0].Value

	sameSite := rawPost(t, e, agentv3connect.AgentServiceGetInfoProcedure,
		map[string]string{"Host": e.browser, "Cookie": session, "Origin": self, "Sec-Fetch-Site": "same-origin"})
	if sameSite.status != http.StatusOK {
		t.Fatalf("same-origin session call: %d", sameSite.status)
	}
	crossSite := rawPost(t, e, agentv3connect.AgentServiceGetInfoProcedure,
		map[string]string{"Host": e.browser, "Cookie": session, "Sec-Fetch-Site": "cross-site"})
	if crossSite.status == http.StatusOK {
		t.Fatal("cross-site cookie request must be rejected")
	}
	mint := rawPost(t, e, agentv3connect.AgentServiceCreateLoginCodeProcedure,
		map[string]string{"Host": e.browser, "Cookie": session, "Origin": self, "Sec-Fetch-Site": "same-origin"})
	if mint.status == http.StatusOK {
		t.Fatal("a session must not mint login codes")
	}
}

// The requests for a sign-in code and a pairing code.
var (
	signInCode = &agentv3.CreateLoginCodeRequest{Purpose: agentv3.LoginCodePurpose_LOGIN_CODE_PURPOSE_SIGN_IN}
	pairCode   = &agentv3.CreateLoginCodeRequest{Purpose: agentv3.LoginCodePurpose_LOGIN_CODE_PURPOSE_PAIR}
)

// exchange trades a sign-in code as a browser at host would.
func exchange(t *testing.T, e *env, code, host, origin string) (int, []*http.Cookie) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.url+agentv3connect.AgentServiceExchangeLoginCodeProcedure,
		strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode, res.Cookies()
}

// signIn opens a browser session and returns its Cookie header.
func signIn(t *testing.T, e *env) string {
	t.Helper()
	lc, err := e.client.CreateLoginCode(context.Background(), signInCode)
	if err != nil {
		t.Fatal(err)
	}
	status, cookies := exchange(t, e, lc.GetCode(), e.browser, "http://"+e.browser)
	if status != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("sign in: status %d cookies %v", status, cookies)
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

func TestSignOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	one, two := signIn(t, e), signIn(t, e)
	sessions := func() int32 {
		t.Helper()
		list, err := e.client.ListDevices(ctx, &agentv3.ListDevicesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return list.GetBrowserSessions()
	}
	if n := sessions(); n != 2 {
		t.Fatalf("browser sessions = %d, want 2", n)
	}
	as := func(cookie string) map[string]string {
		return map[string]string{"Host": e.browser, "Cookie": cookie, "Origin": "http://" + e.browser, "Sec-Fetch-Site": "same-origin"}
	}
	out := rawPost(t, e, agentv3connect.AgentServiceSignOutProcedure, as(one))
	if out.status != http.StatusOK || len(out.cookies) != 1 || out.cookies[0].MaxAge >= 0 {
		t.Fatalf("sign out: status %d cookies %+v; want the cookie cleared", out.status, out.cookies)
	}
	if res := rawPost(t, e, agentv3connect.AgentServiceGetInfoProcedure, as(one)); res.status != http.StatusUnauthorized {
		t.Fatalf("a signed-out browser called the API: %d", res.status)
	}
	if _, err := e.client.SignOut(ctx, &agentv3.SignOutRequest{}); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("the agent token signing out = %v", err)
	}

	ended, err := e.client.SignOutBrowsers(ctx, &agentv3.SignOutBrowsersRequest{})
	if err != nil || ended.GetEnded() != 1 {
		t.Fatalf("sign out every browser = %v, %v", ended, err)
	}
	if res := rawPost(t, e, agentv3connect.AgentServiceGetInfoProcedure, as(two)); res.status != http.StatusUnauthorized {
		t.Fatalf("a browser signed out with the rest called the API: %d", res.status)
	}
	if n := sessions(); n != 0 {
		t.Fatalf("browser sessions after signing out = %d", n)
	}
}

func TestOverviewAndWatch(t *testing.T) {
	e := setup(t)
	seed(t, e.db, "A", false)
	seed(t, e.db, "Q", true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ov, err := e.client.GetOverview(ctx, &agentv3.GetOverviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	o := ov.GetOverview()
	if o.GetAssets().GetDownloaded() != 1 || o.GetAssets().GetQuarantined() != 1 || o.GetArchive().GetFiles() != 2 ||
		o.GetStorage().GetTotalBytes() <= 0 || o.GetLastCycle() != nil ||
		o.GetAppleAccount().GetState() != agentv3.AppleAccountState_APPLE_ACCOUNT_STATE_UNSPECIFIED {
		t.Fatalf("overview = %v", o)
	}

	streamClient := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, e.url), auth.Bearer(e.token)))
	stream, err := streamClient.WatchOverview(ctx, &agentv3.WatchOverviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	first, err := stream.Receive()
	if err != nil || first.GetOverview().GetAssets().GetQuarantined() != 1 {
		t.Fatalf("first overview: %v, %v", first, err)
	}
	if _, err = e.client.ReleaseQuarantine(ctx, &agentv3.ReleaseQuarantineRequest{
		Library: "PrimarySync", AssetId: "Q", Reason: "checked: plays fine",
	}); err != nil {
		t.Fatal(err)
	}
	next, err := stream.Receive()
	if err != nil || next.GetOverview().GetAssets().GetQuarantined() != 0 {
		t.Fatalf("overview after release: %v, %v", next, err)
	}
}

func TestQuarantineActions(t *testing.T) {
	e := setup(t)
	seed(t, e.db, "Q", true)
	ctx := context.Background()

	list, err := e.client.ListQuarantine(ctx, &agentv3.ListQuarantineRequest{})
	if err != nil || len(list.GetAssets()) != 1 {
		t.Fatalf("ListQuarantine = %v, %v", list, err)
	}
	qa := list.GetAssets()[0]
	if qa.GetAsset().GetFilename() != "Q.MOV" || qa.GetFailures()[0].GetTier() != agentv3.Tier_TIER_STRUCTURE {
		t.Fatalf("quarantined asset = %v", qa)
	}
	asset, err := e.client.GetAsset(ctx, &agentv3.GetAssetRequest{Library: "PrimarySync", AssetId: "Q"})
	if err != nil || asset.GetAsset().GetRenditions()[0].GetFailure().GetReason() != "no moov box" ||
		len(asset.GetAsset().GetRenditions()[0].GetRequiredTiers()) != 3 {
		t.Fatalf("GetAsset = %v, %v", asset, err)
	}
	if _, missErr := e.client.GetAsset(ctx, &agentv3.GetAssetRequest{Library: "PrimarySync", AssetId: "nope"}); code(missErr) != connect.CodeNotFound {
		t.Fatalf("missing asset = %v", missErr)
	}
	release := func(reason string) error {
		_, relErr := e.client.ReleaseQuarantine(ctx, &agentv3.ReleaseQuarantineRequest{
			Library: "PrimarySync", AssetId: "Q", Reason: reason,
		})
		return relErr
	}
	if blankErr := release("   "); code(blankErr) != connect.CodeInvalidArgument {
		t.Fatalf("blank reason = %v", blankErr)
	}
	if okErr := release("reviewed"); okErr != nil {
		t.Fatal(okErr)
	}
	if againErr := release("again"); code(againErr) != connect.CodeFailedPrecondition {
		t.Fatalf("second release = %v", againErr)
	}
	events, err := e.client.ListEvents(ctx, &agentv3.ListEventsRequest{
		Kinds: []agentv3.EventKind{agentv3.EventKind_EVENT_KIND_QUARANTINED, agentv3.EventKind_EVENT_KIND_QUARANTINE_RELEASED},
	})
	if err != nil || len(events.GetEvents()) != 2 ||
		events.GetEvents()[0].GetQuarantineReleased().GetReason() != "reviewed" ||
		events.GetEvents()[1].GetQuarantined().GetTier() != agentv3.Tier_TIER_STRUCTURE {
		t.Fatalf("ListEvents = %v, %v", events, err)
	}
	if _, err := e.client.ListEvents(ctx, &agentv3.ListEventsRequest{}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("ListEvents without kinds = %v", err)
	}
}

func TestQuarantineRepair(t *testing.T) {
	e := setup(t)
	seed(t, e.db, "Q", true)
	seed(t, e.db, "P", false)
	ctx := context.Background()
	repair := func(asset string) error {
		_, err := e.client.RepairQuarantine(ctx, &agentv3.RepairQuarantineRequest{
			Library: "PrimarySync", AssetId: asset,
		})
		return err
	}

	if err := repair("Q"); err != nil {
		t.Fatal(err)
	}
	list, err := e.client.ListQuarantine(ctx, &agentv3.ListQuarantineRequest{})
	if err != nil || len(list.GetAssets()) != 1 || list.GetAssets()[0].GetRepairRequestedAt() == nil ||
		list.GetAssets()[0].GetFailures()[0].GetSetAsideAt() != nil {
		t.Fatalf("ListQuarantine = %v, %v; want the repair requested, nothing set aside", list, err)
	}
	if err := repair("Q"); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second repair = %v", err)
	}
	if _, err := e.client.ReleaseQuarantine(ctx, &agentv3.ReleaseQuarantineRequest{
		Library: "PrimarySync", AssetId: "Q", Reason: "fine",
	}); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("release during a repair = %v", err)
	}
	if err := repair("P"); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("repair of a healthy asset = %v", err)
	}
	if err := repair("nope"); code(err) != connect.CodeNotFound {
		t.Fatalf("repair of an unknown asset = %v", err)
	}

	search := func(query string) []*agentv3.Event {
		t.Helper()
		events, err := e.client.ListEvents(ctx, &agentv3.ListEventsRequest{
			Kinds: []agentv3.EventKind{
				agentv3.EventKind_EVENT_KIND_STATE_CHANGED, agentv3.EventKind_EVENT_KIND_QUARANTINE_REPAIR_REQUESTED,
			},
			Query: query,
		})
		if err != nil {
			t.Fatal(err)
		}
		return events.GetEvents()
	}
	got := search("  q.mov ")
	if len(got) != 2 || got[0].GetQuarantineRepair().GetStep() != agentv3.QuarantineRepairStep_QUARANTINE_REPAIR_STEP_REQUESTED ||
		got[0].GetQuarantineRepair().GetVersions()[0] != "original" || got[1].GetAsset().GetAssetId() != "Q" {
		t.Fatalf("search for Q = %v", got)
	}
	if got := search("nothing like it"); len(got) != 0 {
		t.Fatalf("search without a match = %v", got)
	}
}

func TestRunsPagingAndSync(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for range 3 {
		id, err := e.db.StartRun(ctx, app.CycleRunKind, t0)
		if err != nil {
			t.Fatal(err)
		}
		rep := app.CycleReport{KeiOutcome: "auth_required"}
		if err := e.db.FinishRun(ctx, id, true, rep, t0); err != nil {
			t.Fatal(err)
		}
	}
	first, err := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{Kind: agentv3.RunKind_RUN_KIND_CYCLE, PageSize: 2})
	if err != nil || len(first.GetRuns()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("first page = %v, %v", first, err)
	}
	if first.GetRuns()[0].GetCycle().GetDownload() != agentv3.DownloadOutcome_DOWNLOAD_OUTCOME_SIGN_IN_REQUIRED {
		t.Fatalf("run = %v", first.GetRuns()[0])
	}
	rest, err := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{Kind: agentv3.RunKind_RUN_KIND_CYCLE, PageSize: 2, PageToken: first.GetNextPageToken()})
	if err != nil || len(rest.GetRuns()) != 1 || rest.GetNextPageToken() != "" {
		t.Fatalf("second page = %v, %v", rest, err)
	}
	if _, tokErr := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{Kind: agentv3.RunKind_RUN_KIND_CYCLE, PageToken: "!!"}); code(tokErr) != connect.CodeInvalidArgument {
		t.Fatalf("bad token = %v", tokErr)
	}
	if _, kindErr := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{}); code(kindErr) != connect.CodeInvalidArgument {
		t.Fatalf("runs without a kind = %v", kindErr)
	}
	scrubID, err := e.db.StartRun(ctx, app.ScrubRunKind, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.db.FinishRun(ctx, scrubID, false, app.ScrubReport{Eligible: 40, Checked: 2, Passed: 1, Mismatched: 1}, t0); err != nil {
		t.Fatal(err)
	}
	backupID, err := e.db.StartRun(ctx, app.LedgerBackupRunKind, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.db.FinishRun(ctx, backupID, true, app.LedgerBackupReport{File: "b.db", SizeBytes: 9}, t0); err != nil {
		t.Fatal(err)
	}
	scrubs, err := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{Kind: agentv3.RunKind_RUN_KIND_SCRUB})
	if err != nil || len(scrubs.GetRuns()) != 1 || scrubs.GetRuns()[0].GetScrub().GetMismatched() != 1 {
		t.Fatalf("scrub runs = %v, %v", scrubs, err)
	}
	ov, err := e.client.GetOverview(ctx, &agentv3.GetOverviewRequest{})
	if err != nil || ov.GetOverview().GetAppleAccount().GetState() != agentv3.AppleAccountState_APPLE_ACCOUNT_STATE_SIGN_IN_REQUIRED {
		t.Fatalf("apple state = %v, %v", ov, err)
	}
	if o := ov.GetOverview(); o.GetLastScrub().GetScrub().GetEligible() != 40 || o.GetLastLedgerBackup().GetLedgerBackup().GetSizeBytes() != 9 {
		t.Fatalf("overview maintenance = %v", o)
	}
	sync, err := e.client.StartSync(ctx, &agentv3.StartSyncRequest{})
	if err != nil || !sync.GetStarted() {
		t.Fatalf("StartSync = %v, %v", sync, err)
	}
}

func get(t *testing.T, e *env, path, host string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	// Redirects are the response under test, not followed: a test must not
	// depend on this computer resolving homecloud.localhost.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if closeErr := res.Body.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	return res.StatusCode, res.Header, string(body)
}

func TestServesApp(t *testing.T) {
	e := setup(t)
	status, header, body := get(t, e, "/", e.browser)
	if status != http.StatusOK || !strings.Contains(body, `id="root"`) {
		t.Fatalf("GET / = %d %q", status, body)
	}
	// A browser that opens the listen address is sent to the app's own name.
	if status, redirect, _ := get(t, e, "/", ""); status != http.StatusTemporaryRedirect || redirect.Get("Location") != "http://"+e.browser+"/" {
		t.Fatalf("GET / at the listen address = %d %v", status, redirect)
	}
	if !strings.Contains(header.Get("Content-Security-Policy"), "default-src 'self'") || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("index headers = %v", header)
	}
	if status, header, _ := get(t, e, "/assets/index-1.js", ""); status != http.StatusOK || !strings.Contains(header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %v", status, header)
	}
	if status, _, _ := get(t, e, "/nope", ""); status != http.StatusNotFound {
		t.Fatalf("GET /nope = %d", status)
	}
	if status, _, _ := get(t, e, "/", "evil.example"); status != http.StatusForbidden {
		t.Fatalf("GET / with foreign Host = %d", status)
	}
}

func TestLibraryAndSignedThumbnails(t *testing.T) {
	e := setup(t)
	seed(t, e.db, "A", false)
	ctx := context.Background()
	res, err := e.client.ListAssets(ctx, &agentv3.ListAssetsRequest{})
	if err != nil || len(res.GetItems()) != 1 {
		t.Fatalf("ListAssets = %v, %v", res, err)
	}
	item := res.GetItems()[0]
	if item.GetKind() != agentv3.AssetKind_ASSET_KIND_VIDEO || item.GetAsset().GetFilename() != "A.MOV" ||
		!strings.HasPrefix(item.GetThumbnailUrl(), "/media/thumb/PrimarySync/A/original?") {
		t.Fatalf("item = %v", item)
	}
	hidden, err := e.client.ListAssets(ctx, &agentv3.ListAssetsRequest{Hidden: true})
	if err != nil || len(hidden.GetItems()) != 0 {
		t.Fatalf("hidden page = %v, %v", hidden, err)
	}

	// A cached rendering stands in for ffmpeg.
	cached := e.renderer.CachePath(strings.Repeat("a", 64), thumbs.Thumb)
	if err := os.MkdirAll(filepath.Dir(cached), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("RIFF....WEBP"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, header, _ := get(t, e, item.GetThumbnailUrl(), "")
	if status != http.StatusOK || header.Get("Content-Type") != "image/webp" {
		t.Fatalf("signed thumbnail = %d %v", status, header)
	}
	if status, _, _ := get(t, e, strings.Split(item.GetThumbnailUrl(), "?")[0], ""); status != http.StatusForbidden {
		t.Fatalf("unsigned thumbnail = %d", status)
	}
}

// TestErrorsReachTheAppInTheAgentsWords pins what the app shows for each kind
// of failure (LoadError, the toasts, the pairing form): the code and the
// agent's own message. Only a *connect.Error a handler writes keeps its
// message on the wire.
func TestErrorsReachTheAppInTheAgentsWords(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	seed(t, e.db, "V", false)
	anon := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, e.url)))
	cases := []struct {
		name string
		call func() error
		code connect.Code
		msg  string
	}{
		{"refused by the web layer", func() error {
			_, err := anon.GetSettings(ctx, &agentv3.GetSettingsRequest{})
			return err
		}, connect.CodeUnauthenticated, "missing or invalid credentials"},
		{"a bad argument", func() error {
			_, err := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{Kind: agentv3.RunKind_RUN_KIND_CYCLE, PageToken: "!"})
			return err
		}, connect.CodeInvalidArgument, "malformed page_token"},
		{"not found", func() error {
			_, err := e.client.GetAsset(ctx, &agentv3.GetAssetRequest{Library: "PrimarySync", AssetId: "nope"})
			return err
		}, connect.CodeNotFound, "asset PrimarySync/nope: not found"},
		{"a precondition", func() error {
			_, err := e.client.ReleaseQuarantine(ctx, &agentv3.ReleaseQuarantineRequest{Library: "PrimarySync", AssetId: "V", Reason: "fine"})
			return err
		}, connect.CodeFailedPrecondition, "release quarantine: asset PrimarySync/V: asset is not quarantined"},
		{"a refused pairing code", func() error {
			_, err := anon.PairDevice(ctx, &agentv3.PairDeviceRequest{Code: "ABCD-EFGH", Name: "Laptop"})
			return err
		}, connect.CodePermissionDenied, auth.ErrInvalidCode.Error()},
		{"a service the agent depends on", func() error {
			e.notes.failWith(errors.New("ntfy answered 502"))
			_, err := e.client.SendTestNotification(ctx, &agentv3.SendTestNotificationRequest{})
			return err
		}, connect.CodeUnavailable, "send test notification: ntfy answered 502"},
		{"the agent's own failure", func() error {
			if err := e.db.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := e.client.ListRuns(ctx, &agentv3.ListRunsRequest{Kind: agentv3.RunKind_RUN_KIND_CYCLE})
			return err
		}, connect.CodeInternal, ""},
	}
	for _, c := range cases {
		err := c.call()
		ce, ok := errors.AsType[*connect.Error](err)
		if !ok || ce.Code() != c.code {
			t.Fatalf("%s: %v, want %v", c.name, err, c.code)
		}
		// An internal failure's message is the ledger's own text, which the
		// test cannot spell; it must not arrive empty.
		if c.msg == "" && ce.Message() == "" || c.msg != "" && ce.Message() != c.msg {
			t.Fatalf("%s: message %q, want %q", c.name, ce.Message(), c.msg)
		}
	}
}

func TestOverviewReportsMissingArchiveDrive(t *testing.T) {
	e := setup(t, func(a *fakeAgent) { a.archive = errors.New("archive root is on another volume") })
	_, err := e.client.GetOverview(context.Background(), &agentv3.GetOverviewRequest{})
	if code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("GetOverview on a replaced archive volume = %v, want failed_precondition", err)
	}
	// The app's banner shows this message (ui/src/api/overview.tsx).
	if ce, ok := errors.AsType[*connect.Error](err); !ok || ce.Message() != "archive drive not available: archive root is on another volume" {
		t.Fatalf("message = %v, want the reason the app shows", err)
	}
}

func TestOverviewAndEventsOfICloudDrive(t *testing.T) {
	signedIn := time.Date(2026, 10, 6, 11, 30, 0, 0, time.UTC)
	e := setup(t, func(a *fakeAgent) { a.signedIn = signedIn })
	ctx := context.Background()
	f := ledger.DriveFile{
		DriveWSID: "FILE::com.apple.CloudDocs::A", ICloudPath: "iCloud Drive/Docs/tax.pdf",
		LocalPath: "iCloud Drive/Docs/tax.pdf", Etag: "e1", Size: 10, SHA256: strings.Repeat("a", 64),
	}
	if err := e.db.RecordDriveFile(ctx, f, "", signedIn); err != nil {
		t.Fatal(err)
	}
	if err := e.db.MarkDriveFilesDeleted(ctx, []string{f.DriveWSID}, signedIn.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	ov, err := e.client.GetOverview(ctx, &agentv3.GetOverviewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	d := ov.GetOverview().GetIcloudDrive()
	if d.GetFiles() != 1 || d.GetBytes() != 10 || d.GetDeletedInIcloud() != 1 || !d.GetSignedInAt().AsTime().Equal(signedIn) {
		t.Fatalf("icloud drive = %v", d)
	}

	events, err := e.client.ListEvents(ctx, &agentv3.ListEventsRequest{
		Kinds: []agentv3.EventKind{agentv3.EventKind_EVENT_KIND_DRIVE_FILE_ARCHIVED, agentv3.EventKind_EVENT_KIND_DRIVE_FILE_DELETED},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := events.GetEvents()
	if len(got) != 2 || got[0].GetDriveFile().GetChange() != agentv3.DriveFileChange_DRIVE_FILE_CHANGE_DELETED ||
		got[1].GetDriveFile().GetSizeBytes() != 10 || got[0].GetAsset().GetFilename() != "tax.pdf" {
		t.Fatalf("events = %v", got)
	}
}

// seedArchived archives a photo taken at taken, verified a month ago and
// confirmed by a scrub yesterday; added says when it was added to iCloud.
func seedArchived(t *testing.T, db *ledger.DB, assetID string, taken, added time.Time) {
	t.Helper()
	ctx := context.Background()
	r := ledger.Rendition{
		Library: "PrimarySync", AssetID: assetID, Version: "original", Filename: assetID + ".HEIC",
		Downloaded: true, LocalPath: "/archive/Photos/" + assetID + ".HEIC", SizeBytes: 10,
		SHA256: strings.Repeat("a", 64), MediaType: "photo", CreatedAt: taken, AddedAt: &added, SeenAt: t0,
	}
	if _, err := db.Ingest(ctx, []ledger.Rendition{r}, t0); err != nil {
		t.Fatal(err)
	}
	k := ledger.Key{Library: "PrimarySync", AssetID: assetID, Version: "original"}
	if err := db.RecordPass(ctx, k, ledger.TierContent, t0.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordScrub(ctx, k, t0.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestSafeToDelete(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	old := t0.AddDate(-2, 0, 0)
	seedArchived(t, e.db, "A", time.Date(2024, 5, 2, 9, 0, 0, 0, time.UTC), old)
	seedArchived(t, e.db, "B", time.Date(2024, 3, 31, 22, 30, 0, 0, time.UTC), old) // April in Athens
	seedArchived(t, e.db, "C", time.Date(2024, 3, 3, 9, 0, 0, 0, time.UTC), old)
	seedArchived(t, e.db, "R", time.Date(2024, 3, 3, 9, 0, 0, 0, time.UTC), t0.Add(-time.Hour))
	seed(t, e.db, "Q", true)
	athens := &agentv3.GetSafeToDeleteRequest{TimeZone: "Europe/Athens"}

	res, err := e.client.GetSafeToDelete(ctx, athens)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.GetReport(); !strings.Contains(r.GetWithheld(), "not been checked") || r.GetAssets() != 0 {
		t.Fatalf("report before any storage check = %v", r)
	}

	e.agent.hub.SetStorage(health.Report{State: health.StateOK, CheckedAt: t0})
	res, err = e.client.GetSafeToDelete(ctx, athens)
	if err != nil {
		t.Fatal(err)
	}
	want := &agentv3.SafeToDelete{
		Assets: 3, Bytes: 30, NotVerified: 1, TooRecent: 1, MinAgeSeconds: 365 * 24 * 3600,
		Months: []*agentv3.SafeToDeleteMonth{
			{Year: 2024, Month: 5, Assets: 1, Bytes: 10},
			{Year: 2024, Month: 4, Assets: 1, Bytes: 10},
			{Year: 2024, Month: 3, Assets: 1, Bytes: 10},
		},
	}
	got := res.GetReport()
	if got.GetExportPath() != filepath.Join(e.root, "safe-to-delete.csv") {
		t.Errorf("export path = %q", got.GetExportPath())
	}
	got.ExportPath = ""
	if !proto.Equal(got, want) {
		t.Fatalf("report = %v, want %v", got, want)
	}

	for _, zone := range []string{"", "Local", "Mars/Olympus_Mons"} {
		_, zoneErr := e.client.GetSafeToDelete(ctx, &agentv3.GetSafeToDeleteRequest{TimeZone: zone})
		if code(zoneErr) != connect.CodeInvalidArgument {
			t.Errorf("time zone %q: %v, want invalid argument", zone, zoneErr)
		}
	}

	first, err := e.client.ListSafeToDelete(ctx, &agentv3.ListSafeToDeleteRequest{PageSize: 2})
	if err != nil || len(first.GetItems()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("first page = %v, %v", first, err)
	}
	item := first.GetItems()[0]
	if item.GetItem().GetAsset().GetAssetId() != "A" || item.GetSizeBytes() != 10 || item.GetPath() != "/archive/Photos/A.HEIC" ||
		!item.GetAddedAt().AsTime().Equal(old) || !strings.HasPrefix(item.GetItem().GetThumbnailUrl(), "/media/thumb/PrimarySync/A/original?") {
		t.Fatalf("first item = %v", item)
	}
	rest, err := e.client.ListSafeToDelete(ctx, &agentv3.ListSafeToDeleteRequest{PageToken: first.GetNextPageToken()})
	if err != nil || len(rest.GetItems()) != 1 || rest.GetItems()[0].GetItem().GetAsset().GetAssetId() != "C" ||
		rest.GetNextPageToken() != "" {
		t.Fatalf("second page = %v, %v", rest, err)
	}
	if _, err = e.client.ListSafeToDelete(ctx, &agentv3.ListSafeToDeleteRequest{PageToken: "!"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed token = %v", err)
	}

	settings, err := e.client.GetSettings(ctx, &agentv3.GetSettingsRequest{})
	if err != nil || settings.GetSettings().GetSafeToDeleteMinAgeSeconds() != 365*24*3600 {
		t.Fatalf("settings = %v, %v", settings, err)
	}
}
