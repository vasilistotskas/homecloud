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
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vasilistotskas/homecloud/internal/api"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/web"
	"github.com/vasilistotskas/homecloud/ui"
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

	notifier := notify.New(cfg.Notify)
	agent := app.NewAgent(cfg, db, log, notifier, logs.kei, time.Now)

	// The local API is non-critical: if it cannot serve, archiving continues.
	webDone := make(chan struct{})
	go func() {
		defer close(webDone)
		if serveErr := serveAPI(ctx, cfg, cfgPath, db, agent, notifier, log); serveErr != nil {
			log.Error("local API stopped", "err", serveErr)
		}
	}()
	defer func() { stop(); <-webDone }()

	if readyErr := platform.NotifyReady(); readyErr != nil {
		log.Error("service manager readiness notification failed", "err", readyErr)
	}
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

// serveAPI serves the local API until ctx ends. Clients find it through the
// discovery file, which is published only once the listener is bound and
// removed again on shutdown.
func serveAPI(ctx context.Context, cfg *config.Config, cfgPath string, db *ledger.DB, agent *app.Agent, n app.Notifier, log *slog.Logger) (err error) {
	token, err := auth.NewToken()
	if err != nil {
		return err
	}
	authn := auth.New(token, time.Now)
	assets, err := ui.Assets()
	if err != nil {
		return err
	}
	h, err := web.Handler(web.Options{
		Listen: cfg.Web.Listen,
		App:    assets,
		Service: api.New(api.Deps{
			Config: cfg, ConfigPath: cfgPath, AgentVersion: version,
			Ledger: db, Agent: agent, Notifier: n, Auth: authn, Now: time.Now,
		}),
		Auth: authn,
		Log:  log,
	})
	if err != nil {
		return err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Web.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Web.Listen, err)
	}
	endpoint, err := auth.Publish(auth.Endpoint{
		URL: "http://" + cfg.Web.Listen, Token: token, AgentVersion: version, PID: os.Getpid(),
	})
	if err != nil {
		return errors.Join(err, ln.Close())
	}
	defer func() {
		if rmErr := os.Remove(endpoint); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove endpoint file: %w", rmErr))
		}
	}()
	log.Info("local API listening", "addr", cfg.Web.Listen, "endpoint", endpoint)
	return web.Serve(ctx, ln, h, log)
}
