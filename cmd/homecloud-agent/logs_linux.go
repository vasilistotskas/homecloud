package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/logging"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// agentLogs are the agent's log destinations. Under systemd (and in a
// container), stderr is captured by journald or the container runtime, as
// is Go's report of a crash.
type agentLogs struct {
	log *slog.Logger
	kei io.Writer
}

// openLogs ignores the [logs] settings: journald and container runtimes
// rotate by themselves. When stderr is the journal, every line starts with
// its syslog priority (sd-daemon(3): "<3>" error, "<4>" warning, "<6>"
// info), which journald strips and records, so `journalctl -p warning`
// finds the problems.
func openLogs(config.Logs) (*agentLogs, error) {
	journal, err := stderrIsJournal()
	if err != nil {
		return nil, err
	}
	if !journal {
		return &agentLogs{log: slog.New(logging.Handler(slog.NewJSONHandler(os.Stderr, nil))), kei: os.Stderr}, nil
	}
	w := &lockedWriter{w: os.Stderr}
	return &agentLogs{
		log: slog.New(logging.Handler(newPriorityHandler(w))),
		// kei runs at warning level (kei.Sync): its lines are warnings, or
		// errors when it says so.
		kei: &keiLines{w: w},
	}, nil
}

func (*agentLogs) Close() error { return nil }

// stderrIsJournal reports whether stderr is connected to the journal: then
// systemd sets $JOURNAL_STREAM to the device and inode of that stream
// (systemd.exec(5)), which must match stderr's, since a process may inherit
// the variable with other output.
func stderrIsJournal() (bool, error) {
	v := os.Getenv("JOURNAL_STREAM")
	if v == "" {
		return false, nil
	}
	var dev, ino uint64
	if _, err := fmt.Sscanf(v, "%d:%d", &dev, &ino); err != nil {
		return false, fmt.Errorf("JOURNAL_STREAM %q: %w", v, err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(os.Stderr.Fd()), &st); err != nil { //nolint:gosec // a file descriptor fits an int
		return false, fmt.Errorf("stat stderr: %w", err)
	}
	return st.Dev == dev && st.Ino == ino, nil
}

// lockedWriter serializes whole lines from several writers onto w.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// writeLine writes prefix and line to w in one call.
func (l *lockedWriter) writeLine(prefix string, line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(append([]byte(prefix), line...)); err != nil {
		return fmt.Errorf("write log line: %w", err)
	}
	return nil
}

// prefixed writes every record of one JSON handler (log/slog writes a record
// in one Write) after a fixed priority prefix.
type prefixed struct {
	w      *lockedWriter
	prefix string
}

func (p prefixed) Write(b []byte) (int, error) {
	if err := p.w.writeLine(p.prefix, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// priorityHandler writes JSON lines, each after the syslog priority of its
// record's level: one JSON handler per priority, all given the same
// attributes and groups.
type priorityHandler struct {
	err, warn, info, debug slog.Handler
}

func newPriorityHandler(w *lockedWriter) slog.Handler {
	h := func(prefix string) slog.Handler {
		return slog.NewJSONHandler(prefixed{w: w, prefix: prefix}, nil)
	}
	return priorityHandler{err: h("<3>"), warn: h("<4>"), info: h("<6>"), debug: h("<7>")}
}

func (h priorityHandler) pick(l slog.Level) slog.Handler {
	switch {
	case l >= slog.LevelError:
		return h.err
	case l >= slog.LevelWarn:
		return h.warn
	case l >= slog.LevelInfo:
		return h.info
	default:
		return h.debug
	}
}

func (h priorityHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.pick(l).Enabled(ctx, l)
}

func (h priorityHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.pick(r.Level).Handle(ctx, r) //nolint:wrapcheck // a handler passes its inner handler's error on unchanged
}

func (h priorityHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return priorityHandler{
		err: h.err.WithAttrs(attrs), warn: h.warn.WithAttrs(attrs),
		info: h.info.WithAttrs(attrs), debug: h.debug.WithAttrs(attrs),
	}
}

func (h priorityHandler) WithGroup(name string) slog.Handler {
	return priorityHandler{
		err: h.err.WithGroup(name), warn: h.warn.WithGroup(name),
		info: h.info.WithGroup(name), debug: h.debug.WithGroup(name),
	}
}

// keiLines writes kei's output a line at a time: "<3>" before its errors
// (kei's plain format names the level after the timestamp), "<4>" before
// everything else, which kei writes at warning level.
type keiLines struct {
	w    *lockedWriter
	mu   sync.Mutex
	part []byte // an unfinished line
}

// keiError is the level word of kei's error lines.
var keiError = []byte(" ERROR ")

func (k *keiLines) Write(b []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.part = append(k.part, b...)
	var errs []error
	for {
		i := bytes.IndexByte(k.part, '\n')
		if i < 0 {
			break
		}
		line := k.part[:i+1]
		prefix := "<4>"
		if bytes.Contains(line, keiError) {
			prefix = "<3>"
		}
		errs = append(errs, k.w.writeLine(prefix, line))
		k.part = k.part[i+1:]
	}
	if err := errors.Join(errs...); err != nil {
		return 0, err
	}
	return len(b), nil
}

// reportStartupFailure writes to stderr, which journald records.
func reportStartupFailure(err error) {
	if errors.Is(err, platform.ErrAlreadyRunning) {
		_, _ = fmt.Fprintln(os.Stderr, "homecloud-agent: already running")
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "homecloud-agent:", err)
}
