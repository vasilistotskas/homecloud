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
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	agentv2 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v2"
	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v2/agentv2connect"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/archive"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := newApp().Run(ctx, os.Args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "homecloud:", err)
		return 1
	}
	return 0
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:    "homecloud",
		Usage:   "keep a local drive as the verified home of iCloud data",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "path to the homecloud TOML config (default: the per-user config dir)",
				Sources: cli.EnvVars(config.EnvPath),
			},
		},
		Commands: append([]*cli.Command{
			{
				Name:   "doctor",
				Usage:  "run read-only environment checks",
				Action: runDoctor,
			},
			{
				Name:  "archive",
				Usage: "manage the archive's identity",
				Commands: []*cli.Command{
					{
						Name:  "init",
						Usage: "mark a folder as a new homecloud archive and print its id for storage.archive_id (once per archive)",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "root", Required: true, Usage: "the archive root folder (storage.root)"},
						},
						Action: runArchiveInit,
					},
				},
			},
			{
				Name:   "verify",
				Usage:  "ingest kei's manifest and run all outstanding verification (does not contact iCloud)",
				Action: runVerify,
			},
			{
				Name:   "status",
				Usage:  "show asset counts by state",
				Action: runStatus,
			},
			{
				Name:  "quarantine",
				Usage: "review quarantined assets",
				Commands: []*cli.Command{
					{Name: "list", Usage: "list failing renditions of quarantined assets", Action: runQuarantineList},
					{
						Name:  "release",
						Usage: "lift a quarantine after review; the asset is fully re-verified",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "library", Required: true, Usage: "library of the asset (see quarantine list)"},
							&cli.StringFlag{Name: "asset", Required: true, Usage: "asset id"},
							&cli.StringFlag{Name: "reason", Required: true, Usage: "why the quarantine is lifted (audited)"},
						},
						Action: runQuarantineRelease,
					},
				},
			},
			{
				Name:   "open",
				Usage:  "open the web app in your browser, signed in to the running agent",
				Action: runOpen,
			},
			{
				Name:   "notify-test",
				Usage:  "send a test push notification through ntfy",
				Action: runNotifyTest,
			},
			{
				Name:   "install",
				Usage:  "start homecloud-agent (next to this binary) at logon and now (Task Scheduler / systemd user unit)",
				Action: runInstall,
			},
			{
				Name:   "uninstall",
				Usage:  "stop starting homecloud-agent at logon",
				Action: runUninstall,
			},
		}, icloudCommands()...),
	}
}

// configPath resolves the configuration path like the agent does.
func configPath(cmd *cli.Command) (string, error) {
	path, err := config.Resolve(cmd.String("config"))
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("config path %s: %w", path, err)
	}
	return abs, nil
}

func loadConfig(cmd *cli.Command) (*config.Config, error) {
	path, err := configPath(cmd)
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
		return nil, cli.Exit("the homecloud agent is running and owns the ledger: "+use, 1)
	}
	if err != nil {
		return nil, fmt.Errorf("ledger writer lock: %w", err)
	}
	return lock, nil
}

func runVerify(ctx context.Context, cmd *cli.Command) (err error) {
	cfg, err := loadConfig(cmd)
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

	log := slog.New(slog.NewTextHandler(cmd.Root().ErrWriter, nil))
	ingested, err := app.Ingest(ctx, cfg, db, time.Now())
	if err != nil {
		return err
	}
	log.Info("ingested", "assets", ingested.Assets, "renditions", ingested.Renditions,
		"new", ingested.NewRenditions, "changed", ingested.ChangedRenditions)

	started := time.Now()
	verified, err := app.Verify(ctx, cfg, db, log, time.Now, func() {})
	log.Info("verified", "checked", verified.Checked, "passed", verified.Passed,
		"mismatched", verified.Mismatched, "errored", verified.Errored, "elapsed", time.Since(started).Round(time.Second))
	if err != nil {
		return err
	}
	if verified.Mismatched > 0 || verified.Errored > 0 {
		return cli.Exit("verify: some renditions did not pass", 1)
	}
	return nil
}

func runStatus(ctx context.Context, cmd *cli.Command) (err error) {
	cfg, err := loadConfig(cmd)
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
	w := cmd.Root().Writer
	for _, s := range []ledger.AssetState{ledger.StateDiscovered, ledger.StateDownloaded, ledger.StateVerified, ledger.StateQuarantined} {
		if _, err := fmt.Fprintf(w, "%-12s %d\n", s, counts[s]); err != nil {
			return fmt.Errorf("write status: %w", err)
		}
	}
	return nil
}

// agentName names the agent executable and its logon task / systemd unit.
const agentName = "homecloud-agent"

// printf writes command output, wrapping write failures.
// runArchiveInit marks a folder as a new archive. It needs no configuration:
// the id it prints goes into the configuration.
func runArchiveInit(_ context.Context, cmd *cli.Command) error {
	root, err := filepath.Abs(cmd.String("root"))
	if err != nil {
		return fmt.Errorf("archive root: %w", err)
	}
	id, err := archive.Init(root, time.Now())
	if err != nil {
		return err
	}
	return printf(cmd, "%s is homecloud archive %s.\nSet it in config.toml:\n\n[storage]\narchive_id = %q\n", root, id, id)
}

func printf(cmd *cli.Command, format string, args ...any) error {
	if _, err := fmt.Fprintf(cmd.Root().Writer, format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func runQuarantineList(ctx context.Context, cmd *cli.Command) (err error) {
	cfg, err := loadConfig(cmd)
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
		return printf(cmd, "no quarantined assets\n")
	}
	for _, q := range items {
		if err := printf(cmd, "%s  %s  %s\n    %s\n    %s\n", q.Library, q.AssetID, q.Version, q.LocalPath, q.Failure); err != nil {
			return err
		}
	}
	return nil
}

func runQuarantineRelease(ctx context.Context, cmd *cli.Command) (err error) {
	cfg, err := loadConfig(cmd)
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

	library, asset := cmd.String("library"), cmd.String("asset")
	if err := db.ReleaseQuarantine(ctx, library, asset, cmd.String("reason"), time.Now()); err != nil {
		return fmt.Errorf("quarantine release: %w", err)
	}
	return printf(cmd, "released %s/%s; it will be re-verified on the next verify run\n", library, asset)
}

func runNotifyTest(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	msg := notify.TestMsg()
	if sendErr := notify.New(cfg.Notify).Send(ctx, msg); sendErr != nil {
		return fmt.Errorf("notify-test: %w", sendErr)
	}
	return printf(cmd, "sent\n")
}

func runInstall(ctx context.Context, cmd *cli.Command) error {
	if _, err := loadConfig(cmd); err != nil {
		return err
	}
	cfgPath, err := configPath(cmd)
	if err != nil {
		return err
	}
	svc, err := agentService()
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	// The agent loads exactly the configuration validated above.
	svc.Args = []string{"--config", cfgPath}
	if installErr := platform.InstallAgent(ctx, svc); installErr != nil {
		return fmt.Errorf("install: %w", installErr)
	}
	return printf(cmd, "registered and started %q -> %s\n", agentName, svc.Executable)
}

// agentService describes the homecloud-agent executable that sits next to this binary.
func agentService() (platform.AgentService, error) {
	self, err := os.Executable()
	if err != nil {
		return platform.AgentService{}, fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(self)
	agent := filepath.Join(dir, agentName+platform.ExeSuffix)
	if _, statErr := os.Stat(agent); statErr != nil {
		return platform.AgentService{}, fmt.Errorf("agent binary next to homecloud: %w", statErr)
	}
	return platform.AgentService{
		Name:        agentName,
		Description: "homecloud background agent: archive iCloud Photos locally and verify it (https://github.com/vasilistotskas/homecloud)",
		Executable:  agent,
		WorkingDir:  dir,
	}, nil
}

func runUninstall(ctx context.Context, cmd *cli.Command) error {
	if err := platform.UninstallAgent(ctx, agentName); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return printf(cmd, "removed %q\n", agentName)
}

func runDoctor(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	results := doctor.Run(ctx, cfg)
	w := cmd.Root().Writer
	for _, r := range results {
		status, detail := "ok  ", r.Detail
		if !r.OK() {
			status, detail = "FAIL", r.Err.Error()
		}
		if _, err := fmt.Fprintf(w, "[%s] %-16s %s\n", status, r.Name, detail); err != nil {
			return fmt.Errorf("write doctor report: %w", err)
		}
	}
	if err := doctor.Err(results); err != nil {
		return cli.Exit("doctor: one or more checks failed", 1)
	}
	return nil
}

// runOpen asks the running agent for a one-time login code and opens the web
// app with it, so the browser gets its own session without seeing the token.
func runOpen(ctx context.Context, cmd *cli.Command) error {
	ep, err := auth.ReadEndpoint()
	if err != nil {
		return err
	}
	client := agentv2connect.NewAgentServiceClient(http.DefaultClient, ep.URL, connect.WithInterceptors(auth.Bearer(ep.Token)))
	res, err := client.CreateLoginCode(ctx, connect.NewRequest(&agentv2.CreateLoginCodeRequest{}))
	if err != nil {
		return fmt.Errorf("request login code: %w", err)
	}
	link := ep.URL + "/#/connect?code=" + url.QueryEscape(res.Msg.GetCode())
	if err := printf(cmd, "Opening %s\nIf the browser does not open, enter code %s on that page (valid until %s, works once).\n",
		ep.URL, res.Msg.GetCode(), res.Msg.GetExpiresAt().AsTime().Local().Format(time.TimeOnly)); err != nil {
		return err
	}
	return platform.OpenURL(link)
}
