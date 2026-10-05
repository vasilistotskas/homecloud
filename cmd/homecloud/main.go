// Command homecloud keeps a local drive as the verified home of iCloud data.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/doctor"
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
