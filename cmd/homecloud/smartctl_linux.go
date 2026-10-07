package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/smartctl"
)

// smartctlCommands are the container's smartctl sidecar (deploy/compose.yaml).
func smartctlCommands() []*command {
	return []*command{{
		name:  "smartctl",
		usage: "drive health for the container",
		subs: []*command{
			leaf("serve", "answer the agent's drive health requests on smartctl.socket: the smartctl sidecar container, which alone holds the drives", runSmartctlServe),
		},
	}}
}

// runSmartctlServe serves until it is stopped. smartctl that cannot run here
// is logged but served anyway: each answer then says why, and the agent shows
// it where it shows drive health.
func runSmartctlServe(ctx context.Context, c *call) error {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	if cfg.DriveHealth.Source != config.DriveHealthSmartctl || cfg.Smartctl.Socket == "" {
		return fmt.Errorf("drive_health.source must be %q and smartctl.socket set", config.DriveHealthSmartctl)
	}
	log := slog.New(slog.NewJSONHandler(c.stderr, nil))
	if err = smartctl.CheckVersion(ctx, cfg.Smartctl); err != nil {
		log.ErrorContext(ctx, "smartctl cannot run here; every answer says why", "err", err)
	}
	// A socket left by an earlier run would make listening fail.
	switch fi, statErr := os.Lstat(cfg.Smartctl.Socket); {
	case errors.Is(statErr, fs.ErrNotExist):
	case statErr != nil:
		return fmt.Errorf("check %s: %w", cfg.Smartctl.Socket, statErr)
	case fi.Mode().Type() == fs.ModeSocket:
		if err = os.Remove(cfg.Smartctl.Socket); err != nil {
			return fmt.Errorf("remove the old socket: %w", err)
		}
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", cfg.Smartctl.Socket)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Smartctl.Socket, err)
	}
	// Only this user (the agent runs as the same one) may connect.
	if err = os.Chmod(cfg.Smartctl.Socket, 0o600); err != nil {
		return errors.Join(fmt.Errorf("restrict %s: %w", cfg.Smartctl.Socket, err), ln.Close())
	}
	log.InfoContext(ctx, "smartctl sidecar listening", "socket", cfg.Smartctl.Socket, "version", version)
	s := &smartctl.Server{
		Cfg: cfg.Smartctl, Node: platform.DriveNode, Read: smartctl.Read, Version: smartctl.CheckVersion, Log: log,
	}
	err = s.Serve(ctx, ln)
	log.InfoContext(context.WithoutCancel(ctx), "smartctl sidecar stopped")
	return err
}
