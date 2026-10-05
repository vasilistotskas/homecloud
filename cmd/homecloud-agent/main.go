// Command homecloud-agent is homecloud's long-running background process.
//
// Windows: build with -ldflags=-H=windowsgui (no console); started at logon by a
// Task Scheduler task. Linux: runs as a systemd user unit (Type=notify) and logs
// to stderr for journald. See docs/design.md §8a.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const instanceName = "homecloud-agent"

func main() {
	if err := run(); err != nil {
		reportStartupFailure(err)
		os.Exit(1)
	}
}

func run() (err error) {
	lock, err := platform.AcquireInstance(instanceName)
	if err != nil {
		return fmt.Errorf("acquire instance lock: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Release()) }()

	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logs := openLogs(cfg.Storage.Root)
	defer func() { err = errors.Join(err, logs.Close()) }()
	log := slog.New(slog.NewJSONHandler(logs.agent, nil))
	log.Info("homecloud-agent starting", "version", version, "config", cfgPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if doctorErr := doctor.Err(doctor.Run(ctx, cfg)); doctorErr != nil {
		log.Error("environment checks failed; not starting", "err", doctorErr)
		return fmt.Errorf("doctor: %w", doctorErr)
	}

	db, err := ledger.Open(ctx, app.LedgerPath(cfg))
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	// The status page is non-critical: if it cannot serve, archiving continues.
	webDone := make(chan struct{})
	go func() {
		defer close(webDone)
		if serveErr := web.Serve(ctx, cfg.Web.Listen, db, log); serveErr != nil {
			log.Error("status page stopped", "err", serveErr)
		}
	}()
	defer func() { stop(); <-webDone }()

	if readyErr := platform.NotifyReady(); readyErr != nil {
		log.Error("service manager readiness notification failed", "err", readyErr)
	}
	agent := &app.Agent{Cfg: cfg, DB: db, Log: log, Notify: notify.New(cfg.Notify), KeiLog: logs.kei, Now: time.Now}
	runErr := agent.Run(ctx)
	if stopErr := platform.NotifyStopping(); stopErr != nil {
		log.Error("service manager stopping notification failed", "err", stopErr)
	}
	if errors.Is(runErr, context.Canceled) {
		log.Info("homecloud-agent stopped")
		return nil
	}
	return runErr
}

func configPath() (string, error) {
	if p := os.Getenv("HOMECLOUD_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "homecloud", "config.toml"), nil
}
