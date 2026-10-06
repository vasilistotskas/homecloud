package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/logfile"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// agentLogs are the agent's log destinations. A windowsgui process has no
// console, so Windows logs go to size-rotated files under <storage.root>\logs
// (package logfile).
type agentLogs struct {
	log     *slog.Logger
	kei     io.Writer
	closers []io.Closer
}

func openLogs(root string, cfg config.Logs) (*agentLogs, error) {
	dir := filepath.Join(root, "logs")
	l := &agentLogs{}
	// A log that cannot be rotated is reported in the agent's own log;
	// logfile reports outside its lock, once per run of failures, so this
	// may also be the agent log reporting about itself.
	opts := logfile.Options{
		MaxBytes:   cfg.MaxBytes(),
		MaxBackups: cfg.MaxBackups,
		OnError:    func(err error) { l.log.Warn("log rotation failed", "err", err) },
	}
	agent, err := logfile.New(filepath.Join(dir, "homecloud-agent.log"), opts)
	if err != nil {
		return nil, fmt.Errorf("agent log: %w", err)
	}
	kei, err := logfile.New(filepath.Join(dir, "kei.log"), opts)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("kei log: %w", err), agent.Close())
	}
	l.log = slog.New(slog.NewJSONHandler(agent, nil))
	l.kei = kei
	l.closers = []io.Closer{agent, kei}
	return l, nil
}

func (l *agentLogs) Close() error {
	var errs []error
	for _, c := range l.closers {
		errs = append(errs, c.Close())
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("close logs: %w", err)
	}
	return nil
}

// reportStartupFailure records errors that happen before the agent log exists
// in %LOCALAPPDATA%\homecloud\agent-startup.log.
func reportStartupFailure(err error) {
	if errors.Is(err, platform.ErrAlreadyRunning) {
		return
	}
	dir, dirErr := platform.StateDir()
	if dirErr != nil {
		return
	}
	path := filepath.Join(dir, "agent-startup.log")
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // fixed name in the per-user state dir
	if openErr != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s homecloud-agent: %v\n", time.Now().Format(time.RFC3339), err)
	_ = f.Close()
}
