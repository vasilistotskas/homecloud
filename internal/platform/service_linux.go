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
	"unicode"
)

// On Linux the agent runs as a systemd user unit (systemd.service(5)), enabled
// for default.target so it starts with the user's service manager. Headless
// hosts additionally need `loginctl enable-linger` (an operator decision).

// InstallAgent writes the user unit, reloads systemd and enables + starts it.
func InstallAgent(ctx context.Context, svc AgentService) error {
	unit, err := renderUnit(svc)
	if err != nil {
		return err
	}
	path, err := unitPath(svc.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // standard systemd user unit dir permissions
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(path, unit, 0o644); err != nil { //nolint:gosec // unit files are not secret
		return fmt.Errorf("write unit: %w", err)
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return systemctl(ctx, "enable", "--now", svc.Name+".service")
}

// UninstallAgent disables and stops the unit and removes its file.
func UninstallAgent(ctx context.Context, name string) error {
	if err := systemctl(ctx, "disable", "--now", name+".service"); err != nil {
		return err
	}
	path, err := unitPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove unit: %w", err)
	}
	return systemctl(ctx, "daemon-reload")
}

func unitPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "systemd", "user", name+".service"), nil
}

// renderUnit builds the unit file. Type=notify makes systemd wait for READY=1,
// so startup failures (volume, lock, config) surface as unit failures.
func renderUnit(svc AgentService) ([]byte, error) {
	if !filepath.IsAbs(svc.Executable) || !filepath.IsAbs(svc.WorkingDir) {
		return nil, fmt.Errorf("agent executable and working dir must be absolute: %q, %q", svc.Executable, svc.WorkingDir)
	}
	for _, s := range append([]string{svc.Description, svc.Executable, svc.WorkingDir}, svc.Args...) {
		if strings.ContainsFunc(s, unicode.IsControl) {
			return nil, fmt.Errorf("unit values must not contain control characters: %q", s)
		}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\n\n", escapeSpecifiers(svc.Description))
	exec := []string{quoteExecArg(svc.Executable)}
	for _, a := range svc.Args {
		exec = append(exec, quoteExecArg(a))
	}
	fmt.Fprintf(&b, "[Service]\nType=notify\nExecStart=%s\nWorkingDirectory=%s\nRestart=on-failure\nRestartSec=10s\n\n",
		strings.Join(exec, " "), escapeSpecifiers(svc.WorkingDir))
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.Bytes(), nil
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
