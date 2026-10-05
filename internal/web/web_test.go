package web_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v1"
	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v1/agentv1connect"
	"github.com/vasilistotskas/homecloud/internal/api"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/web"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fakeAgent struct {
	hub     *app.Hub
	started bool
}

func (f *fakeAgent) Hub() *app.Hub      { return f.hub }
func (f *fakeAgent) RequestCycle() bool { return f.started }

type nopNotifier struct{}

func (nopNotifier) Send(context.Context, notify.Msg) error { return nil }

type env struct {
	url    string
	host   string
	token  string
	db     *ledger.DB
	agent  *fakeAgent
	client agentv1connect.AgentServiceClient
}

func setup(t *testing.T) *env {
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
	authn := auth.New(token, func() time.Time { return t0 })
	agent := &fakeAgent{hub: app.NewHub(), started: true}
	cfg := &config.Config{
		Storage:  config.Storage{Root: t.TempDir(), MinFreePercent: 15},
		Web:      config.Web{Listen: listen},
		Schedule: config.Schedule{SyncInterval: config.Duration(time.Hour)},
	}
	h, err := web.Handler(web.Options{
		Listen: listen,
		Service: api.New(api.Deps{
			Config: cfg, ConfigPath: "config.toml", AgentVersion: "test",
			Ledger: db, Agent: agent, Notifier: nopNotifier{}, Auth: authn,
			Now: func() time.Time { return t0 },
		}),
		Auth: authn,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &env{
		url: srv.URL, host: listen, token: token, db: db, agent: agent,
		client: agentv1connect.NewAgentServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(bearer(token))),
	}
}

func bearer(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	})
}

// streamBearer adds the token to streaming calls, which unary interceptors skip.
type streamBearer struct{ token string }

func (streamBearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

func (s streamBearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+s.token)
		return conn
	}
}

func (streamBearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
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
	anon := agentv1connect.NewAgentServiceClient(http.DefaultClient, e.url)
	_, err := anon.GetInfo(context.Background(), connect.NewRequest(&agentv1.GetInfoRequest{}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous GetInfo = %v, want unauthenticated", err)
	}
	wrong := agentv1connect.NewAgentServiceClient(http.DefaultClient, e.url, connect.WithInterceptors(bearer("nope")))
	if _, wrongErr := wrong.GetInfo(context.Background(), connect.NewRequest(&agentv1.GetInfoRequest{})); code(wrongErr) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token = %v, want unauthenticated", wrongErr)
	}
	info, err := e.client.GetInfo(context.Background(), connect.NewRequest(&agentv1.GetInfoRequest{}))
	if err != nil || info.Msg.GetApiVersion() != api.Version {
		t.Fatalf("GetInfo = %v, %v", info, err)
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
	auth := "Bearer " + e.token
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"own host", map[string]string{"Authorization": auth}, http.StatusOK},
		{"rebinding host", map[string]string{"Authorization": auth, "Host": "evil.example:80"}, http.StatusForbidden},
		{"foreign origin", map[string]string{"Authorization": auth, "Origin": "https://evil.example"}, http.StatusForbidden},
		{"desktop origin", map[string]string{"Authorization": auth, "Origin": "http://tauri.localhost"}, http.StatusOK},
	}
	for _, c := range cases {
		res := rawPost(t, e, agentv1connect.AgentServiceGetInfoProcedure, c.hdr)
		if res.status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, res.status, c.want)
		}
	}
}

func TestDesktopPreflight(t *testing.T) {
	e := setup(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, e.url+agentv1connect.AgentServiceGetInfoProcedure, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://tauri.localhost")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	// Browsers send the list lowercased and sorted (Fetch standard), which rs/cors relies on.
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
}

func TestBrowserLogin(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	lc, err := e.client.CreateLoginCode(ctx, connect.NewRequest(&agentv1.CreateLoginCodeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	self := "http://" + e.host

	bad := rawPost(t, e, agentv1connect.AgentServiceExchangeLoginCodeProcedure, map[string]string{"Origin": self})
	if bad.status == http.StatusOK || len(bad.cookies) != 0 {
		t.Fatalf("empty code: status %d cookies %v", bad.status, bad.cookies)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+agentv1connect.AgentServiceExchangeLoginCodeProcedure,
		strings.NewReader(`{"code":"`+lc.Msg.GetCode()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", self)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	cookies := res.Cookies()
	if res.StatusCode != http.StatusOK || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("exchange: status %d cookies %+v", res.StatusCode, cookies)
	}
	session := cookies[0].Name + "=" + cookies[0].Value

	sameSite := rawPost(t, e, agentv1connect.AgentServiceGetInfoProcedure,
		map[string]string{"Cookie": session, "Origin": self, "Sec-Fetch-Site": "same-origin"})
	if sameSite.status != http.StatusOK {
		t.Fatalf("same-origin session call: %d", sameSite.status)
	}
	crossSite := rawPost(t, e, agentv1connect.AgentServiceGetInfoProcedure,
		map[string]string{"Cookie": session, "Sec-Fetch-Site": "cross-site"})
	if crossSite.status == http.StatusOK {
		t.Fatal("cross-site cookie request must be rejected")
	}
	mint := rawPost(t, e, agentv1connect.AgentServiceCreateLoginCodeProcedure,
		map[string]string{"Cookie": session, "Origin": self, "Sec-Fetch-Site": "same-origin"})
	if mint.status == http.StatusOK {
		t.Fatal("a session must not mint login codes")
	}
}

func TestOverviewAndWatch(t *testing.T) {
	e := setup(t)
	seed(t, e.db, "A", false)
	seed(t, e.db, "Q", true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ov, err := e.client.GetOverview(ctx, connect.NewRequest(&agentv1.GetOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	o := ov.Msg.GetOverview()
	if o.GetAssets().GetDownloaded() != 1 || o.GetAssets().GetQuarantined() != 1 || o.GetArchive().GetFiles() != 2 ||
		o.GetStorage().GetTotalBytes() <= 0 || o.GetLastCycle() != nil ||
		o.GetAppleAccount().GetState() != agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_UNSPECIFIED {
		t.Fatalf("overview = %v", o)
	}

	streamClient := agentv1connect.NewAgentServiceClient(http.DefaultClient, e.url, connect.WithInterceptors(streamBearer{e.token}))
	stream, err := streamClient.WatchOverview(ctx, connect.NewRequest(&agentv1.WatchOverviewRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() || stream.Msg().GetOverview().GetAssets().GetQuarantined() != 1 {
		t.Fatalf("first overview: %v", stream.Err())
	}
	if _, err := e.client.ReleaseQuarantine(ctx, connect.NewRequest(&agentv1.ReleaseQuarantineRequest{
		Library: "PrimarySync", AssetId: "Q", Reason: "checked: plays fine",
	})); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetOverview().GetAssets().GetQuarantined() != 0 {
		t.Fatalf("overview after release: %v", stream.Err())
	}
}

func TestQuarantineActions(t *testing.T) {
	e := setup(t)
	seed(t, e.db, "Q", true)
	ctx := context.Background()

	list, err := e.client.ListQuarantine(ctx, connect.NewRequest(&agentv1.ListQuarantineRequest{}))
	if err != nil || len(list.Msg.GetAssets()) != 1 {
		t.Fatalf("ListQuarantine = %v, %v", list, err)
	}
	qa := list.Msg.GetAssets()[0]
	if qa.GetAsset().GetFilename() != "Q.MOV" || qa.GetFailures()[0].GetTier() != agentv1.Tier_TIER_STRUCTURE {
		t.Fatalf("quarantined asset = %v", qa)
	}
	asset, err := e.client.GetAsset(ctx, connect.NewRequest(&agentv1.GetAssetRequest{Library: "PrimarySync", AssetId: "Q"}))
	if err != nil || asset.Msg.GetAsset().GetRenditions()[0].GetFailure().GetReason() != "no moov box" ||
		len(asset.Msg.GetAsset().GetRenditions()[0].GetRequiredTiers()) != 3 {
		t.Fatalf("GetAsset = %v, %v", asset, err)
	}
	if _, missErr := e.client.GetAsset(ctx, connect.NewRequest(&agentv1.GetAssetRequest{Library: "PrimarySync", AssetId: "nope"})); code(missErr) != connect.CodeNotFound {
		t.Fatalf("missing asset = %v", missErr)
	}
	release := func(reason string) error {
		_, relErr := e.client.ReleaseQuarantine(ctx, connect.NewRequest(&agentv1.ReleaseQuarantineRequest{
			Library: "PrimarySync", AssetId: "Q", Reason: reason,
		}))
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
	events, err := e.client.ListEvents(ctx, connect.NewRequest(&agentv1.ListEventsRequest{
		Kinds: []agentv1.EventKind{agentv1.EventKind_EVENT_KIND_QUARANTINED, agentv1.EventKind_EVENT_KIND_QUARANTINE_RELEASED},
	}))
	if err != nil || len(events.Msg.GetEvents()) != 2 ||
		events.Msg.GetEvents()[0].GetQuarantineReleased().GetReason() != "reviewed" ||
		events.Msg.GetEvents()[1].GetQuarantined().GetTier() != agentv1.Tier_TIER_STRUCTURE {
		t.Fatalf("ListEvents = %v, %v", events, err)
	}
	if _, err := e.client.ListEvents(ctx, connect.NewRequest(&agentv1.ListEventsRequest{})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("ListEvents without kinds = %v", err)
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
	first, err := e.client.ListRuns(ctx, connect.NewRequest(&agentv1.ListRunsRequest{PageSize: 2}))
	if err != nil || len(first.Msg.GetRuns()) != 2 || first.Msg.GetNextPageToken() == "" {
		t.Fatalf("first page = %v, %v", first, err)
	}
	if first.Msg.GetRuns()[0].GetCycle().GetDownload() != agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_SIGN_IN_REQUIRED {
		t.Fatalf("run = %v", first.Msg.GetRuns()[0])
	}
	rest, err := e.client.ListRuns(ctx, connect.NewRequest(&agentv1.ListRunsRequest{PageSize: 2, PageToken: first.Msg.GetNextPageToken()}))
	if err != nil || len(rest.Msg.GetRuns()) != 1 || rest.Msg.GetNextPageToken() != "" {
		t.Fatalf("second page = %v, %v", rest, err)
	}
	if _, tokErr := e.client.ListRuns(ctx, connect.NewRequest(&agentv1.ListRunsRequest{PageToken: "!!"})); code(tokErr) != connect.CodeInvalidArgument {
		t.Fatalf("bad token = %v", tokErr)
	}
	ov, err := e.client.GetOverview(ctx, connect.NewRequest(&agentv1.GetOverviewRequest{}))
	if err != nil || ov.Msg.GetOverview().GetAppleAccount().GetState() != agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_SIGN_IN_REQUIRED {
		t.Fatalf("apple state = %v, %v", ov, err)
	}
	sync, err := e.client.StartSync(ctx, connect.NewRequest(&agentv1.StartSyncRequest{}))
	if err != nil || !sync.Msg.GetStarted() {
		t.Fatalf("StartSync = %v, %v", sync, err)
	}
}
