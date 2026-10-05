// Command homecloud-agent is homecloud's background process. Build it with
// -ldflags=-H=windowsgui so Windows never allocates a console; it is started at
// logon by a Task Scheduler logon trigger (see docs/design.md, Spike C).
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

	"github.com/DeRuina/timberjack"

	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
	"github.com/vasilistotskas/homecloud/internal/instance"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	mutexName     = "homecloud-agent"
	logMaxSizeMB  = 20
	logMaxBackups = 10
)

func main() {
	if err := run(); err != nil {
		reportStartupFailure(err)
		os.Exit(1)
	}
}

func run() (err error) {
	lock, err := instance.Acquire(mutexName)
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

	logDir := filepath.Join(cfg.Storage.Root, "logs")
	agentLog := &timberjack.Logger{Filename: filepath.Join(logDir, "homecloud-agent.log"), MaxSize: logMaxSizeMB, MaxBackups: logMaxBackups, LocalTime: true}
	keiLog := &timberjack.Logger{Filename: filepath.Join(logDir, "kei.log"), MaxSize: logMaxSizeMB, MaxBackups: logMaxBackups, LocalTime: true}
	defer func() { err = errors.Join(err, agentLog.Close(), keiLog.Close()) }()
	log := slog.New(slog.NewJSONHandler(agentLog, nil))
	log.Info("homecloud-agent starting", "version", version, "config", cfgPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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

	agent := &app.Agent{Cfg: cfg, DB: db, Log: log, Notify: notify.New(cfg.Notify), KeiLog: keiLog, Now: time.Now}
	err = agent.Run(ctx)
	if errors.Is(err, context.Canceled) {
		log.Info("homecloud-agent stopped")
		return nil
	}
	return err
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

// reportStartupFailure records errors that happen before (or instead of) the
// agent's own log; a windowsgui process has no console to print to.
func reportStartupFailure(err error) {
	if errors.Is(err, instance.ErrAlreadyRunning) {
		return
	}
	dir, dirErr := os.UserCacheDir()
	if dirErr != nil {
		return
	}
	path := filepath.Join(dir, "homecloud", "agent-startup.log")
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // fixed path under the user cache dir
	if openErr != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s homecloud-agent: %v\n", time.Now().Format(time.RFC3339), err)
	_ = f.Close()
}
