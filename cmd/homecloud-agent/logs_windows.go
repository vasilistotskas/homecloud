package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/logfile"
	"github.com/vasilistotskas/homecloud/internal/logging"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// Files in the log folder besides the rotated logs.
const (
	// crashFile receives Go's report of a crash (an unhandled panic or a
	// fatal error), which would otherwise be lost: a windowsgui process has
	// no console. The next start reports it and keeps it as
	// homecloud-agent-crash-<UTC time>.log.
	crashFile   = "homecloud-agent-crash.log"
	crashPrefix = "homecloud-agent-crash-"
	// startupFile records why the agent could not start before its log was
	// open (an unreadable configuration, for example).
	startupFile = "agent-startup.log"
)

// agentLogs are the agent's log destinations. A windowsgui process has no
// console, so Windows logs go to size-rotated files (package logfile) in
// %LOCALAPPDATA%\homecloud\logs: off the archive drive, so the agent can
// still log that the drive is missing.
type agentLogs struct {
	log     *slog.Logger
	kei     io.Writer
	closers []io.Closer
}

// logDir is the folder of the agent's logs.
func logDir() (string, error) {
	state, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "logs"), nil
}

func openLogs(cfg config.Logs) (*agentLogs, error) {
	dir, err := logDir()
	if err != nil {
		return nil, err
	}
	l := &agentLogs{}
	// A log that cannot be rotated is reported in the agent's own log;
	// logfile reports outside its lock, once per run of failures, so this
	// may also be the agent log reporting about itself.
	opts := logfile.Options{
		MaxBytes:   cfg.MaxBytes(),
		MaxBackups: cfg.MaxBackups,
		OnError:    func(err error) { l.log.Warn("log not rotated", "err", err) },
	}
	agent, err := logfile.New(filepath.Join(dir, "homecloud-agent.log"), opts)
	if err != nil {
		return nil, fmt.Errorf("agent log: %w", err)
	}
	kei, err := logfile.New(filepath.Join(dir, "kei.log"), opts)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("kei log: %w", err), agent.Close())
	}
	l.log = slog.New(logging.Handler(slog.NewJSONHandler(agent, nil)))
	l.kei = kei
	l.closers = []io.Closer{agent, kei}
	if err := l.catchCrashes(dir, cfg.MaxBackups); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	return l, nil
}

// catchCrashes reports the crash the previous agent process left, if any,
// and sends this process's crash report to dir. It keeps the newest keep
// crash reports.
func (l *agentLogs) catchCrashes(dir string, keep int) error {
	path := filepath.Join(dir, crashFile)
	switch cause, err := firstLine(path); {
	case errors.Is(err, fs.ErrNotExist), err == nil && cause == "":
	case err != nil:
		return err
	default:
		kept := filepath.Join(dir, crashPrefix+time.Now().UTC().Format(platform.FileTimeLayout)+".log")
		if err := os.Rename(path, kept); err != nil {
			return fmt.Errorf("keep the crash report: %w", err)
		}
		l.log.Error("the previous agent process crashed", "cause", cause, "report", kept)
		if err := pruneCrashes(dir, keep); err != nil {
			l.log.Warn("old crash reports not deleted", "err", err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the log folder: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // fixed name in the per-user log folder
	if err != nil {
		return fmt.Errorf("open the crash report: %w", err)
	}
	// SetCrashOutput keeps its own copy of the file descriptor.
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		return errors.Join(fmt.Errorf("send crash reports to %s: %w", path, err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close the crash report: %w", err)
	}
	return nil
}

// firstLine is the first non-empty line of the file at path: for a Go crash
// report, "panic: …" or "fatal error: …".
func firstLine(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // fixed name in the per-user log folder
	if err != nil {
		return "", fmt.Errorf("read the crash report: %w", err)
	}
	defer f.Close() //nolint:errcheck // read only
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read the crash report: %w", err)
	}
	return "", nil
}

// pruneCrashes deletes all but the newest keep crash reports in dir; their
// names sort in time order.
func pruneCrashes(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list the log folder: %w", err)
	}
	var reports []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), crashPrefix) && strings.HasSuffix(e.Name(), ".log") {
			reports = append(reports, e.Name())
		}
	}
	slices.Sort(reports)
	var errs []error
	for _, name := range reports[:max(0, len(reports)-keep)] {
		errs = append(errs, os.Remove(filepath.Join(dir, name)))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("delete old crash reports: %w", err)
	}
	return nil
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

// reportStartupFailure records errors that happen before the agent log is
// open in the log folder's agent-startup.log.
func reportStartupFailure(err error) {
	if errors.Is(err, platform.ErrAlreadyRunning) {
		return
	}
	dir, dirErr := logDir()
	if dirErr != nil {
		return
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, startupFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // fixed name in the per-user log folder
	if openErr != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s homecloud-agent: %v\n", time.Now().Format(time.RFC3339), err)
	_ = f.Close()
}
