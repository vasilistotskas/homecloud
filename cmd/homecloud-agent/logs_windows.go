package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/DeRuina/timberjack"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

const (
	logMaxSizeMB  = 20
	logMaxBackups = 10
)

// agentLogs are the agent's log destinations. A windowsgui process has no
// console, so Windows logs go to rotating files under <storage.root>\logs.
type agentLogs struct {
	agent, kei io.Writer
	closers    []io.Closer
}

func openLogs(root string) *agentLogs {
	dir := filepath.Join(root, "logs")
	agent := &timberjack.Logger{Filename: filepath.Join(dir, "homecloud-agent.log"), MaxSize: logMaxSizeMB, MaxBackups: logMaxBackups, LocalTime: true}
	kei := &timberjack.Logger{Filename: filepath.Join(dir, "kei.log"), MaxSize: logMaxSizeMB, MaxBackups: logMaxBackups, LocalTime: true}
	return &agentLogs{agent: agent, kei: kei, closers: []io.Closer{agent, kei}}
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
