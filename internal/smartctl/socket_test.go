package smartctl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// sidecar serves a fake smartctl on a socket in a temporary folder: sdb is
// passed in and healthy, sdc is not passed in, sdd's smartctl may not run.
func sidecar(t *testing.T, version error) (config.Smartctl, *[]string) {
	t.Helper()
	cfg := config.Smartctl{Timeout: config.Duration(10 * time.Second), Socket: filepath.Join(t.TempDir(), "s.sock")}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", cfg.Socket)
	if err != nil {
		t.Fatal(err)
	}
	var read []string
	temp := 34
	s := &Server{
		Cfg: cfg,
		Node: func(name string) (string, error) {
			if name == "sdc" {
				return "", nil
			}
			return "/dev/" + name, nil
		},
		Read: func(_ context.Context, _ config.Smartctl, d platform.Drive) (platform.DriveHealth, error) {
			read = append(read, d.Name)
			switch {
			case d.Device == "":
				return platform.DriveHealth{}, fmt.Errorf("%w: no device node here", platform.ErrHealthUnsupported)
			case d.Name == "sdd":
				return platform.DriveHealth{}, ErrNotPermitted
			}
			return platform.DriveHealth{Model: "TEST", Protocol: "ATA", TemperatureCelsius: &temp, Warnings: []string{"failing"}}, nil
		},
		Version: func(context.Context, config.Smartctl) error { return version },
		Log:     slog.New(slog.DiscardHandler),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return cfg, &read
}

func TestSidecarReadsDrives(t *testing.T) {
	cfg, read := sidecar(t, nil)
	ctx := context.Background()
	temp := 34
	got, err := RemoteRead(ctx, cfg, platform.Drive{Name: "sdb"})
	want := platform.DriveHealth{Model: "TEST", Protocol: "ATA", TemperatureCelsius: &temp, Warnings: []string{"failing"}}
	if err != nil || !cmp.Equal(want, got) {
		t.Fatalf("sdb = %+v, %v", got, err)
	}
	if _, err := RemoteRead(ctx, cfg, platform.Drive{Name: "sdc"}); !errors.Is(err, platform.ErrHealthUnsupported) {
		t.Fatalf("a drive not passed in = %v, want ErrHealthUnsupported", err)
	}
	if _, err := RemoteRead(ctx, cfg, platform.Drive{Name: "sdd"}); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("smartctl without its capabilities = %v, want ErrNotPermitted", err)
	}
	for _, name := range []string{"../../etc/passwd", "SDA", "sda/../sdb", strings.Repeat("a", 40)} {
		if _, err := RemoteRead(ctx, cfg, platform.Drive{Name: name}); err == nil || !strings.Contains(err.Error(), "not a drive name") {
			t.Fatalf("drive %q = %v, want refused", name, err)
		}
	}
	if !cmp.Equal([]string{"sdb", "sdc", "sdd"}, *read) {
		t.Fatalf("smartctl ran for %v", *read)
	}
}

func TestSidecarVersion(t *testing.T) {
	cfg, _ := sidecar(t, nil)
	if err := RemoteCheckVersion(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _ = sidecar(t, ErrNotPermitted)
	if err := RemoteCheckVersion(context.Background(), cfg); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("version = %v, want ErrNotPermitted", err)
	}
}

func TestSidecarRefusesLargeRequests(t *testing.T) {
	cfg, read := sidecar(t, nil)
	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "unix", cfg.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // test connection
	if _, err := conn.Write([]byte(`{"drive":"` + strings.Repeat("a", 2*maxRequest) + `"}`)); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, maxResponse)
	n, _ := conn.Read(b) //nolint:errcheck // the answer is checked below
	if !strings.Contains(string(b[:n]), "request too large") || len(*read) != 0 {
		t.Fatalf("answer = %q, smartctl ran for %v", b[:n], *read)
	}
}

func TestSidecarAbsent(t *testing.T) {
	cfg := config.Smartctl{Timeout: config.Duration(10 * time.Second), Socket: filepath.Join(t.TempDir(), "none.sock")}
	if _, err := RemoteRead(context.Background(), cfg, platform.Drive{Name: "sdb"}); err == nil ||
		!strings.Contains(err.Error(), "reach the smartctl sidecar") {
		t.Fatalf("no sidecar = %v", err)
	}
}
