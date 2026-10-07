package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// On Linux tasks are systemd user units (systemd.service(5)): the agent is a
// service enabled for default.target, so it starts with the user's service
// manager; a job is a oneshot service a timer (systemd.timer(5)) starts every
// Task.Every. Headless hosts additionally need `loginctl enable-linger` (an
// operator decision).

// InstallTask writes the user units, reloads systemd and enables + starts the
// service (the agent) or its timer (a job).
func InstallTask(ctx context.Context, t Task) error {
	unit, err := renderUnit(t)
	if err != nil {
		return err
	}
	if err := writeUnit(t.Name+".service", unit); err != nil {
		return err
	}
	start := t.Name + ".service"
	if t.Every != 0 {
		if err := writeUnit(t.Name+".timer", renderTimer(t)); err != nil {
			return err
		}
		start = t.Name + ".timer"
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return systemctl(ctx, "enable", "--now", start)
}

// UninstallTask disables and stops the task (its timer, if it has one) and
// removes its unit files.
func UninstallTask(ctx context.Context, name string) error {
	dir, err := unitDir()
	if err != nil {
		return err
	}
	units := []string{name + ".service"}
	if _, err := os.Stat(filepath.Join(dir, name+".timer")); err == nil {
		units = []string{name + ".timer", name + ".service"}
	}
	for _, u := range units {
		if err := systemctl(ctx, "disable", "--now", u); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, u)); err != nil {
			return fmt.Errorf("remove unit %s: %w", u, err)
		}
	}
	return systemctl(ctx, "daemon-reload")
}

func unitDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "systemd", "user"), nil
}

func writeUnit(name string, body []byte) error {
	dir, err := unitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // standard systemd user unit dir permissions
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil { //nolint:gosec // unit files are not secret
		return fmt.Errorf("write unit %s: %w", name, err)
	}
	return nil
}

// renderUnit builds the service unit. The agent's Type=notify makes systemd
// wait for READY=1, so startup failures (volume, lock, config) surface as unit
// failures; a job is a oneshot service its timer starts, so it has no
// [Install] section of its own.
func renderUnit(t Task) ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	for _, s := range append([]string{t.Description, t.Executable, t.WorkingDir}, t.Args...) {
		if strings.ContainsFunc(s, unicode.IsControl) {
			return nil, fmt.Errorf("unit values must not contain control characters: %q", s)
		}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\n\n", escapeSpecifiers(t.Description))
	exec := []string{quoteExecArg(t.Executable)}
	for _, a := range t.Args {
		exec = append(exec, quoteExecArg(a))
	}
	if t.Every != 0 {
		fmt.Fprintf(&b, "[Service]\nType=oneshot\nExecStart=%s\nWorkingDirectory=%s\n",
			strings.Join(exec, " "), escapeSpecifiers(t.WorkingDir))
		return b.Bytes(), nil
	}
	fmt.Fprintf(&b, "[Service]\nType=notify\nExecStart=%s\nWorkingDirectory=%s\nRestart=on-failure\nRestartSec=10s\n\n",
		strings.Join(exec, " "), escapeSpecifiers(t.WorkingDir))
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.Bytes(), nil
}

// renderTimer builds a job's timer: it starts the job a minute after the
// user's service manager starts, then Every after each run ends, so runs
// never overlap.
func renderTimer(t Task) []byte {
	return fmt.Appendf(nil, "[Unit]\nDescription=%s\n\n[Timer]\nOnStartupSec=1min\nOnUnitInactiveSec=%dmin\n\n[Install]\nWantedBy=timers.target\n",
		escapeSpecifiers(t.Description), t.Every/time.Minute)
}

// quoteExecArg quotes one ExecStart= item per systemd.syntax(7) "Quoting" and
// escapes specifier (%) and environment ($) expansion per systemd.service(5).
func quoteExecArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

// escapeSpecifiers escapes % for settings that expand specifiers but are not quoted.
func escapeSpecifiers(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}

func systemctl(ctx context.Context, args ...string) error {
	var out bytes.Buffer
	cmd := Command(ctx, "systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out.Bytes()))
	}
	return nil
}

// NotifyReady tells systemd the agent finished starting (sd_notify(3) READY=1).
func NotifyReady() error { return sdNotify("READY=1") }

// NotifyStopping tells systemd the agent is shutting down (STOPPING=1).
func NotifyStopping() error { return sdNotify("STOPPING=1") }

// sdNotify implements the sd_notify datagram protocol, which sd_notify(3)
// documents as stable for independent reimplementation. Outside systemd
// ($NOTIFY_SOCKET unset) there is no service manager to inform.
func sdNotify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	if !strings.HasPrefix(addr, "/") && !strings.HasPrefix(addr, "@") {
		return fmt.Errorf("unsupported NOTIFY_SOCKET address %q", addr)
	}
	// Go maps a leading '@' to the Linux abstract socket namespace.
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("dial NOTIFY_SOCKET: %w", err)
	}
	_, writeErr := conn.Write([]byte(state))
	if err := errors.Join(writeErr, conn.Close()); err != nil {
		return fmt.Errorf("sd_notify %s: %w", state, err)
	}
	return nil
}
