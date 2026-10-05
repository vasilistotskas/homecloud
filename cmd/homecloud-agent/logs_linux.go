package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// agentLogs are the agent's log destinations. Under systemd, stderr is
// captured by journald (journalctl --user -u homecloud-agent).
type agentLogs struct {
	agent, kei io.Writer
}

func openLogs(string) *agentLogs {
	return &agentLogs{agent: os.Stderr, kei: os.Stderr}
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
