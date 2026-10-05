// Command homecloud keeps a local drive as the verified home of iCloud data.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/schedtask"
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
				Usage:   "path to the homecloud TOML config",
				Value:   defaultConfigPath(),
				Sources: cli.EnvVars("HOMECLOUD_CONFIG"),
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "doctor",
				Usage:  "run read-only environment checks",
				Action: runDoctor,
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
				Name:   "notify-test",
				Usage:  "send a test push notification through ntfy",
				Action: runNotifyTest,
			},
			{
				Name:   "install",
				Usage:  "register homecloud-agent (next to this binary) to start at logon, and start it now",
				Action: runInstall,
			},
			{
				Name:   "uninstall",
				Usage:  "remove the homecloud-agent logon task",
				Action: runUninstall,
			},
		},
	}
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "homecloud", "config.toml")
}

func loadConfig(cmd *cli.Command) (*config.Config, error) {
	path := cmd.String("config")
	if path == "" {
		return nil, fmt.Errorf("no config path: set --config or HOMECLOUD_CONFIG")
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

func runVerify(ctx context.Context, cmd *cli.Command) (err error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
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
	verified, err := app.Verify(ctx, cfg, db, log, time.Now)
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

// agentTaskName is the Task Scheduler name of the logon task.
const agentTaskName = "homecloud-agent"

// printf writes command output, wrapping write failures.
func printf(cmd *cli.Command, format string, args ...any) error {
	if _, err := fmt.Fprintf(cmd.Root().Writer, format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func runNotifyTest(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	msg := notify.Msg{Title: "homecloud test", Body: "Notifications from homecloud reach this device.", Priority: notify.PriorityDefault, Tags: []string{"white_check_mark"}}
	if sendErr := notify.New(cfg.Notify).Send(ctx, msg); sendErr != nil {
		return fmt.Errorf("notify-test: %w", sendErr)
	}
	return printf(cmd, "sent\n")
}

func runInstall(ctx context.Context, cmd *cli.Command) error {
	if _, err := loadConfig(cmd); err != nil {
		return err
	}
	spec, err := agentTaskSpec()
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if regErr := schedtask.Register(ctx, spec); regErr != nil {
		return fmt.Errorf("install: %w", regErr)
	}
	if runErr := schedtask.Run(ctx, agentTaskName); runErr != nil {
		return fmt.Errorf("install: start agent: %w", runErr)
	}
	return printf(cmd, "registered and started task %q -> %s\n", agentTaskName, spec.Command)
}

// agentTaskSpec describes the logon task for the homecloud-agent.exe that sits
// next to this binary, running as the current user.
func agentTaskSpec() (schedtask.Spec, error) {
	self, err := os.Executable()
	if err != nil {
		return schedtask.Spec{}, fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(self)
	agent := filepath.Join(dir, "homecloud-agent.exe")
	if _, statErr := os.Stat(agent); statErr != nil {
		return schedtask.Spec{}, fmt.Errorf("agent binary next to homecloud.exe: %w", statErr)
	}
	sid, user, err := schedtask.CurrentUser()
	if err != nil {
		return schedtask.Spec{}, err
	}
	return schedtask.Spec{
		Name:        agentTaskName,
		Description: "homecloud background agent: archive iCloud Photos to the local archive and verify it. https://github.com/vasilistotskas/homecloud",
		Command:     agent,
		WorkingDir:  dir,
		UserSID:     sid,
		UserName:    user,
	}, nil
}

func runUninstall(ctx context.Context, cmd *cli.Command) error {
	if err := schedtask.Delete(ctx, agentTaskName); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return printf(cmd, "removed task %q (a running agent keeps running until it exits or you log off)\n", agentTaskName)
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
