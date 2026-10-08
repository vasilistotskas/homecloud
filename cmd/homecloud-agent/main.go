// Command homecloud-agent is homecloud's long-running background process.
//
// Windows: build with -ldflags=-H=windowsgui (no console); started at logon by a
// Task Scheduler task. Linux: runs as a systemd user unit (Type=notify) and logs
// to stderr for journald. See docs/design.md §8a.
package main

import (
	"context"
	"errors"
	"flag"
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
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/media"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
	"github.com/vasilistotskas/homecloud/internal/web"
	"github.com/vasilistotskas/homecloud/ui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		if !errors.Is(err, errLogged) {
			reportStartupFailure(err)
		}
		os.Exit(1)
	}
}

// errLogged marks an error the agent's log already holds.
var errLogged = errors.New("in the agent log")

func run() (err error) {
	lock, err := platform.AcquireInstance(app.LedgerWriterLock)
	if err != nil {
		return fmt.Errorf("acquire instance lock: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Release()) }()

	flags := flag.NewFlagSet("homecloud-agent", flag.ContinueOnError)
	configFlag := flags.String("config", "", "path to the homecloud TOML config (default: $"+config.EnvPath+", then the per-user config dir)")
	if err = flags.Parse(os.Args[1:]); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	cfgPath, err := config.Resolve(*configFlag)
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logs, err := openLogs(cfg.Logs)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, logs.Close()) }()
	log := logs.log
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.InfoContext(ctx, "homecloud-agent starting", "version", version, "pid", os.Getpid(), "config", cfgPath)
	// How the process ends, in one line. On Windows the agent is usually
	// ended without notice (logoff, shutdown, schtasks /End), which writes no
	// line: the next start reports the runs it left unfinished.
	started := false
	defer func() {
		end := context.WithoutCancel(ctx)
		switch {
		case err != nil && !started:
			log.ErrorContext(end, "homecloud-agent not started", "err", err)
		case err != nil:
			log.ErrorContext(end, "homecloud-agent stopped", "err", err)
		default:
			log.InfoContext(end, "homecloud-agent stopped")
		}
		if err != nil {
			err = errors.Join(errLogged, err)
		}
	}()

	// homecloud owns kei's configuration: write it before anything checks or runs kei.
	if changed, keiErr := kei.WriteConfig(cfg); keiErr != nil {
		return fmt.Errorf("write kei configuration: %w", keiErr)
	} else if changed {
		log.InfoContext(ctx, "kei configuration written", "path", cfg.Kei.Config)
	}

	// The archive holds the only copy of the library and the ledger backups all
	// it knows: nobody but this account (with SYSTEM and Administrators) may
	// read or change them. kei's folder was made private as its configuration
	// was written; a backup folder not created yet is made private by the
	// first backup.
	private := []string{cfg.Storage.Root}
	if _, statErr := os.Stat(cfg.Backup.Dir); statErr == nil {
		private = append(private, cfg.Backup.Dir)
	}
	for _, dir := range private {
		if changed, privErr := platform.MakePrivate(dir); privErr != nil {
			return fmt.Errorf("make %s private: %w", dir, privErr)
		} else if changed {
			log.InfoContext(ctx, "folder made private to this account", "path", dir)
		}
	}

	if doctorErr := doctor.Err(doctor.Run(ctx, cfg)); doctorErr != nil {
		return fmt.Errorf("environment checks (homecloud doctor): %w", doctorErr)
	}

	db, err := ledger.Open(ctx, app.LedgerPath(cfg))
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	notifier := notify.New(cfg.Notify)
	renderer := thumbs.New(cfg.FFmpeg, app.ThumbnailDir(cfg), log)
	// The cache is derived data: a failed prune costs disk space, not correctness.
	if removed, pruneErr := renderer.Prune(); pruneErr != nil {
		log.ErrorContext(ctx, "thumbnail cache not pruned", "err", pruneErr)
	} else if removed > 0 {
		log.InfoContext(ctx, "thumbnail cache pruned", "superseded", removed)
	}
	agent := app.NewAgent(cfg, db, log, notifier, logs.kei, renderer, time.Now)
	agent.SetConfigPath(cfgPath)

	// The local API is non-critical: if it cannot serve, archiving continues.
	webDone := make(chan struct{})
	go func() {
		defer close(webDone)
		if serveErr := serveAPI(ctx, cfg, cfgPath, db, agent, notifier, renderer, log); serveErr != nil {
			log.ErrorContext(ctx, "local API stopped; archiving goes on", "err", serveErr)
		}
	}()
	defer func() { stop(); <-webDone }()

	if readyErr := platform.NotifyReady(); readyErr != nil {
		log.ErrorContext(ctx, "service manager not told the agent is ready", "err", readyErr)
	}
	started = true
	runErr := agent.Run(ctx)
	if stopErr := platform.NotifyStopping(); stopErr != nil {
		log.ErrorContext(ctx, "service manager not told the agent is stopping", "err", stopErr)
	}
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// serveAPI serves the local API until ctx ends. Clients find it through the
// discovery file, which is published only once the listener is bound and
// removed again on shutdown.
func serveAPI(ctx context.Context, cfg *config.Config, cfgPath string, db *ledger.DB, agent *app.Agent, n app.Notifier,
	renderer *thumbs.Renderer, log *slog.Logger,
) (err error) {
	token, err := auth.NewToken()
	if err != nil {
		return err
	}
	authn := auth.New(token, cfg.Web.SessionTTL.Std(), *cfg.Web.PublicOrigin != "", db, time.Now)
	signer, err := media.NewSigner(time.Now)
	if err != nil {
		return err
	}
	assets, err := ui.Assets()
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the agent executable: %w", err)
	}
	h, err := web.Handler(web.Options{
		Listen:       cfg.Web.Listen,
		PublicOrigin: *cfg.Web.PublicOrigin,
		App:          assets,
		Service: api.New(api.Deps{
			Config: cfg, ConfigPath: cfgPath, AgentVersion: version,
			// The CLI is installed next to the agent.
			CLI:    filepath.Join(filepath.Dir(self), "homecloud"+platform.ExeSuffix),
			Ledger: db, Agent: agent, Notifier: n, Auth: authn, Media: signer, Log: log, Now: time.Now,
		}),
		Media: media.Handler(signer, authn, db, renderer, log),
		Auth:  authn,
		Log:   log,
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
		URL: "http://" + cfg.Web.Listen, Token: token, AgentVersion: version,
	})
	if err != nil {
		return errors.Join(err, ln.Close())
	}
	defer func() {
		if rmErr := os.Remove(endpoint); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove endpoint file: %w", rmErr))
		}
	}()
	log.InfoContext(ctx, "local API listening", "addr", cfg.Web.Listen, "endpoint", endpoint)
	return web.Serve(ctx, ln, h, log)
}
