// Command homecloud keeps a local drive as the verified home of iCloud data.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3/agentv3connect"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/archive"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := newApp().execute(ctx, &call{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "homecloud:", err)
	}
	return exitStatus(err)
}

// leaf is a command without flags of its own.
func leaf(name, usage string, run action) *command {
	return &command{name: name, usage: usage, define: func(*flags) action { return run }}
}

func newApp() *program {
	return &program{
		name:    "homecloud",
		summary: "keep a local drive as the verified home of iCloud data",
		version: version,
		root: append([]*command{
			leaf("doctor", "run read-only environment checks", runDoctor),
			{
				name:  "archive",
				usage: "manage the archive's identity",
				subs: []*command{{
					name:  "init",
					usage: "mark a folder as a new homecloud archive and print its id for storage.archive_id (once per archive)",
					define: func(f *flags) action {
						root := f.requiredString("root", "the archive root folder (storage.root)")
						return func(ctx context.Context, c *call) error { return runArchiveInit(ctx, c, *root) }
					},
				}},
			},
			leaf("verify", "ingest kei's manifest and run all outstanding verification (does not contact iCloud)", runVerify),
			leaf("status", "show asset counts by state", runStatus),
			{
				name:  "quarantine",
				usage: "review quarantined assets",
				subs: []*command{
					leaf("list", "list failing renditions of quarantined assets", runQuarantineList),
					{
						name:  "release",
						usage: "lift a quarantine after review; the asset is fully re-verified",
						define: func(f *flags) action {
							library := f.requiredString("library", "library of the asset (see quarantine list)")
							asset := f.requiredString("asset", "asset id")
							reason := f.requiredString("reason", "why the quarantine is lifted (audited)")
							return func(ctx context.Context, c *call) error {
								return runQuarantineRelease(ctx, c, *library, *asset, *reason)
							}
						},
					},
					{
						name:  "repair",
						usage: "download the failing files again; the agent's next sync moves them to the archive's Damaged folder first",
						define: func(f *flags) action {
							library := f.requiredString("library", "library of the asset (see quarantine list)")
							asset := f.requiredString("asset", "asset id")
							return func(ctx context.Context, c *call) error {
								return runQuarantineRepair(ctx, c, *library, *asset)
							}
						},
					},
				},
			},
			{
				name:  "open",
				usage: "open the web app in your browser, signed in to the running agent",
				define: func(f *flags) action {
					printLink := f.Bool("print", false, "print the one-time sign-in link instead of opening a browser (for another device, or a headless host)")
					return func(ctx context.Context, c *call) error { return runOpen(ctx, c, *printLink) }
				},
			},
			leaf("pair", "print a one-time code that pairs the desktop app on another computer with this agent (needs web.public_origin)", runPair),
			leaf("notify-test", "send a test push notification through ntfy", runNotifyTest),
			leaf("install", "start homecloud-agent (next to this binary) at logon and now (Task Scheduler / systemd user unit)", runInstall),
			leaf("uninstall", "stop starting homecloud-agent at logon", runUninstall),
		}, slices.Concat(icloudCommands(), iphoneCommands(), smartctlCommands())...),
	}
}

// configPath resolves the configuration path like the agent does.
func configPath(c *call) (string, error) {
	path, err := config.Resolve(c.config)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("config path %s: %w", path, err)
	}
	return abs, nil
}

func loadConfig(c *call) (*config.Config, error) {
	path, err := configPath(c)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func openLedger(ctx context.Context, cfg *config.Config) (*ledger.DB, error) {
	db, err := ledger.Open(ctx, app.LedgerPath(cfg))
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	return db, nil
}

// acquireLedgerWriter takes the ledger-writer lock, or explains that the agent
// holds it. Only one process writes the ledger at a time.
func acquireLedgerWriter(use string) (*platform.Lock, error) {
	lock, err := platform.AcquireInstance(app.LedgerWriterLock)
	if errors.Is(err, platform.ErrAlreadyRunning) {
		return nil, errors.New("the homecloud agent is running and owns the ledger: " + use)
	}
	if err != nil {
		return nil, fmt.Errorf("ledger writer lock: %w", err)
	}
	return lock, nil
}

func runVerify(ctx context.Context, c *call) (err error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	lock, err := acquireLedgerWriter("it verifies every cycle; use Sync now in the app (homecloud open)")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	db, err := openLedger(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	log := slog.New(slog.NewTextHandler(c.stderr, nil))
	ingested, err := app.Ingest(ctx, cfg, db, time.Now())
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "ingested", "assets", ingested.Assets, "renditions", ingested.Renditions,
		"new", ingested.NewRenditions, "changed", ingested.ChangedRenditions)

	started := time.Now()
	verified, err := app.Verify(ctx, cfg, db, log, time.Now, func() {})
	log.InfoContext(ctx, "verified", "checked", verified.Checked, "passed", verified.Passed,
		"mismatched", verified.Mismatched, "errored", verified.Errored, "elapsed", time.Since(started).Round(time.Second))
	if err != nil {
		return err
	}
	if verified.Mismatched > 0 || verified.Errored > 0 {
		return errors.New("verify: some renditions did not pass")
	}
	return nil
}

func runStatus(ctx context.Context, c *call) (err error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	db, err := openLedger(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	counts, err := db.StateCounts(ctx)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	for _, s := range []ledger.AssetState{ledger.StateDiscovered, ledger.StateDownloaded, ledger.StateVerified, ledger.StateQuarantined} {
		if err := printf(c, "%-12s %d\n", s, counts[s]); err != nil {
			return err
		}
	}
	return nil
}

// agentName names the agent executable and its logon task / systemd unit.
const agentName = "homecloud-agent"

// runArchiveInit marks a folder as a new archive. It needs no configuration:
// the id it prints goes into the configuration.
func runArchiveInit(_ context.Context, c *call, rootFlag string) error {
	root, err := filepath.Abs(rootFlag)
	if err != nil {
		return fmt.Errorf("archive root: %w", err)
	}
	id, err := archive.Init(root, time.Now())
	if err != nil {
		return err
	}
	return printf(c, "%s is homecloud archive %s.\nSet it in config.toml:\n\n[storage]\narchive_id = %q\n", root, id, id)
}

// printf writes command output, wrapping write failures.
func printf(c *call, format string, args ...any) error {
	if _, err := fmt.Fprintf(c.stdout, format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func runQuarantineList(ctx context.Context, c *call) (err error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	db, err := openLedger(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	items, err := db.Quarantined(ctx)
	if err != nil {
		return fmt.Errorf("quarantine list: %w", err)
	}
	if len(items) == 0 {
		return printf(c, "no quarantined assets\n")
	}
	for _, q := range items {
		if err := printf(c, "%s  %s  %s\n    %s\n    %s\n%s", q.Library, q.AssetID, q.Version, q.LocalPath, q.Failure,
			repairLine(q.Repair)); err != nil {
			return err
		}
	}
	return nil
}

// repairLine describes the progress of a repair, or is empty without one.
func repairLine(r *ledger.Repair) string {
	switch {
	case r == nil:
		return ""
	case r.SetAsideAt == nil:
		return fmt.Sprintf("    repair requested %s; the agent's next sync moves the file aside\n",
			r.RequestedAt.Local().Format(time.DateTime))
	case r.KeptAs == "":
		return "    repair: the file was missing; waiting for kei to download it\n"
	default:
		return fmt.Sprintf("    repair: moved to %s; waiting for kei to download it\n", r.KeptAs)
	}
}

func runQuarantineRelease(ctx context.Context, c *call, library, asset, reason string) (err error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	lock, err := acquireLedgerWriter("release quarantine in the app (homecloud open)")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	db, err := openLedger(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	if err := db.ReleaseQuarantine(ctx, library, asset, reason, time.Now()); err != nil {
		return fmt.Errorf("quarantine release: %w", err)
	}
	return printf(c, "released %s/%s; it will be re-verified on the next verify run\n", library, asset)
}

func runQuarantineRepair(ctx context.Context, c *call, library, asset string) (err error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	lock, err := acquireLedgerWriter("repair it in the app (homecloud open)")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	db, err := openLedger(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	if err := db.RequestRepair(ctx, library, asset, time.Now()); err != nil {
		return fmt.Errorf("quarantine repair: %w", err)
	}
	return printf(c, "repair of %s/%s requested; the agent's next sync downloads its failing files again\n", library, asset)
}

func runNotifyTest(ctx context.Context, c *call) error {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	msg := notify.TestMsg()
	if sendErr := notify.New(cfg.Notify).Send(ctx, msg); sendErr != nil {
		return fmt.Errorf("notify-test: %w", sendErr)
	}
	return printf(c, "sent\n")
}

func runInstall(ctx context.Context, c *call) error {
	if _, err := loadConfig(c); err != nil {
		return err
	}
	cfgPath, err := configPath(c)
	if err != nil {
		return err
	}
	task, err := agentTask()
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	// The agent loads exactly the configuration validated above.
	task.Args = []string{"--config", cfgPath}
	if installErr := platform.InstallTask(ctx, task); installErr != nil {
		return fmt.Errorf("install: %w", installErr)
	}
	return printf(c, "registered and started %q -> %s\n", agentName, task.Executable)
}

// agentTask describes the homecloud-agent executable that sits next to this binary.
func agentTask() (platform.Task, error) {
	self, err := os.Executable()
	if err != nil {
		return platform.Task{}, fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(self)
	agent := filepath.Join(dir, agentName+platform.ExeSuffix)
	if _, statErr := os.Stat(agent); statErr != nil {
		return platform.Task{}, fmt.Errorf("agent binary next to homecloud: %w", statErr)
	}
	return platform.Task{
		Name:        agentName,
		Description: "homecloud background agent: archive iCloud Photos locally and verify it (https://github.com/vasilistotskas/homecloud)",
		Executable:  agent,
		WorkingDir:  dir,
	}, nil
}

func runUninstall(ctx context.Context, c *call) error {
	if err := platform.UninstallTask(ctx, agentName); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return printf(c, "removed %q\n", agentName)
}

func runDoctor(ctx context.Context, c *call) error {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	results := doctor.Run(ctx, cfg)
	for _, r := range results {
		status, detail := "ok  ", r.Detail
		if !r.OK() {
			status, detail = "FAIL", r.Err.Error()
		}
		if err := printf(c, "[%s] %-16s %s\n", status, r.Name, detail); err != nil {
			return err
		}
	}
	if doctor.Err(results) != nil {
		return errors.New("doctor: one or more checks failed")
	}
	return nil
}

// loginCode asks the running agent for a one-time code for purpose.
func loginCode(ctx context.Context, purpose agentv3.LoginCodePurpose) (*agentv3.CreateLoginCodeResponse, error) {
	ep, err := auth.ReadEndpoint()
	if err != nil {
		return nil, err
	}
	client := agentv3connect.NewAgentServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, ep.URL), auth.Bearer(ep.Token)))
	res, err := client.CreateLoginCode(ctx, &agentv3.CreateLoginCodeRequest{Purpose: purpose})
	if err != nil {
		return nil, fmt.Errorf("request login code: %w", err)
	}
	return res, nil
}

// runOpen asks the running agent for a one-time sign-in code and opens the
// web app with it, so the browser gets its own session without seeing the
// token.
func runOpen(ctx context.Context, c *call, printLink bool) error {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	msg, err := loginCode(ctx, agentv3.LoginCodePurpose_LOGIN_CODE_PURPOSE_SIGN_IN)
	if err != nil {
		return err
	}
	// Behind a proxy the app is used at its public origin; on this computer
	// at its own name (web.BrowserHost), where its cookie reaches no other
	// loopback service.
	base := *cfg.Web.PublicOrigin
	if base == "" {
		addr, addrErr := web.BrowserAddr(cfg.Web.Listen)
		if addrErr != nil {
			return addrErr
		}
		base = "http://" + addr
	}
	code, until := msg.GetCode(), msg.GetExpiresAt().AsTime().Local().Format(time.TimeOnly)
	link := base + "/#/connect?code=" + url.QueryEscape(code)
	if printLink {
		return printf(c, "Sign in at %s\n(or enter code %s at %s; valid until %s, works once)\n", link, code, base, until)
	}
	if err := printf(c, "Opening %s\nIf the browser does not open, enter code %s on that page (valid until %s, works once).\n",
		base, code, until); err != nil {
		return err
	}
	return platform.OpenURL(link)
}

// runPair prints a one-time pairing code for the desktop app on another
// computer. A paired device keeps its access until it is revoked, so its
// code is never a sign-in code, which is shown in links and on screens.
func runPair(ctx context.Context, c *call) error {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	origin := *cfg.Web.PublicOrigin
	if origin == "" {
		return errors.New("pairing needs web.public_origin: another computer reaches this agent only there")
	}
	msg, err := loginCode(ctx, agentv3.LoginCodePurpose_LOGIN_CODE_PURPOSE_PAIR)
	if err != nil {
		return err
	}
	return printf(c, "Pairing code %s (valid until %s, works once).\n"+
		"In the desktop app on the other computer: Settings → Connection → Connect to another device,\n"+
		"address %s, then this code.\n", msg.GetCode(), msg.GetExpiresAt().AsTime().Local().Format(time.TimeOnly), origin)
}
