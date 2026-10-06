package platform

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const sampleMountinfo = `22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/root rw,errors=remount-ro
41 22 0:45 / /mnt/archive\040disk rw,relatime shared:30 - btrfs /dev/nvme0n1p1 rw,ssd
57 22 0:26 / /run/user/1000 rw,nosuid shared:5 - tmpfs tmpfs rw,size=1G
`

func TestParseMount(t *testing.T) {
	tests := []struct {
		id   uint64
		want mount
	}{
		// /dev/root is not a device node: the device number identifies the volume.
		{22, mount{dev: unix.Mkdev(8, 2), fsType: "ext4", source: "/dev/root"}},
		// btrfs reports an anonymous device number; its source names the device.
		{41, mount{dev: unix.Mkdev(0, 45), fsType: "btrfs", source: "/dev/nvme0n1p1"}},
		{57, mount{dev: unix.Mkdev(0, 26), fsType: "tmpfs", source: "tmpfs"}},
	}
	for _, tt := range tests {
		got, err := parseMount(strings.NewReader(sampleMountinfo), tt.id)
		if err != nil || got != tt.want {
			t.Errorf("parseMount(%d) = %+v, %v; want %+v", tt.id, got, err, tt.want)
		}
	}
	if _, err := parseMount(strings.NewReader(sampleMountinfo), 99); err == nil {
		t.Error("unknown mount id accepted")
	}
	if _, err := parseMount(strings.NewReader("7 1 8:1 / / rw\n"), 7); err == nil {
		t.Error("line without separator accepted")
	}
	if _, err := parseMount(strings.NewReader("7 1 eight / / rw - ext4 /dev/sda1 rw\n"), 7); err == nil {
		t.Error("malformed device number accepted")
	}
}

func TestRenderUnit(t *testing.T) {
	unit, err := renderUnit(AgentService{
		Name:        "homecloud-agent",
		Description: "homecloud agent (100% local)",
		Executable:  `/opt/home cloud/bin/homecloud-agent`,
		Args:        []string{"--config", "/home/me/.config/homecloud/config.toml"},
		WorkingDir:  `/opt/home cloud/bin`,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Description=homecloud agent (100%% local)",
		"Type=notify",
		`ExecStart="/opt/home cloud/bin/homecloud-agent" "--config" "/home/me/.config/homecloud/config.toml"`,
		"WorkingDirectory=/opt/home cloud/bin",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if _, err := renderUnit(AgentService{Executable: "rel/agent", WorkingDir: "/x"}); err == nil {
		t.Error("relative executable accepted")
	}
	if _, err := renderUnit(AgentService{Executable: "/x/a\nb", WorkingDir: "/x"}); err == nil {
		t.Error("control character accepted")
	}
}

func TestQuoteExecArg(t *testing.T) {
	if got, want := quoteExecArg(`/a "b"\c%d$e`), `"/a \"b\"\\c%%d$$e"`; got != want {
		t.Errorf("quoteExecArg = %s, want %s", got, want)
	}
}

func TestSdNotify(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	t.Setenv("NOTIFY_SOCKET", sock)
	if notifyErr := NotifyReady(); notifyErr != nil {
		t.Fatalf("NotifyReady: %v", notifyErr)
	}
	buf := make([]byte, 64)
	if deadlineErr := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("received %q, %v", buf[:n], err)
	}
}

func TestSdNotifyWithoutSystemd(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := NotifyStopping(); err != nil {
		t.Fatalf("NotifyStopping outside systemd: %v", err)
	}
}

func TestAcquireInstanceRequiresRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := AcquireInstance("x"); err == nil {
		t.Fatal("want error without XDG_RUNTIME_DIR")
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	l, err := AcquireInstance("x")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "homecloud", "x.lock")); err != nil { //nolint:gosec // test-controlled temp dir
		t.Fatalf("lock file missing: %v", err)
	}
}
