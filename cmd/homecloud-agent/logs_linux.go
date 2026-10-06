package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// agentLogs are the agent's log destinations. Under systemd (and in a
// container), stderr is captured by journald or the container runtime.
type agentLogs struct {
	log *slog.Logger
	kei io.Writer
}

// openLogs ignores the [logs] settings: journald and container runtimes
// rotate by themselves.
func openLogs(string, config.Logs) (*agentLogs, error) {
	return &agentLogs{log: slog.New(slog.NewJSONHandler(os.Stderr, nil)), kei: os.Stderr}, nil
}

func (*agentLogs) Close() error { return nil }

// reportStartupFailure writes to stderr, which journald records.
func reportStartupFailure(err error) {
	if errors.Is(err, platform.ErrAlreadyRunning) {
		_, _ = fmt.Fprintln(os.Stderr, "homecloud-agent: already running")
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "homecloud-agent:", err)
}
