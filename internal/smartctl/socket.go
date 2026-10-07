package smartctl

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// The container's smartctl sidecar. smartctl needs the drives and the
// capabilities to send them SMART commands, which the agent's container must
// not have: it parses media downloaded from iCloud. `homecloud smartctl
// serve` runs smartctl in a container of its own (no network, the drives
// only) and answers on a Unix socket in a volume the two share
// (smartctl.socket); the agent asks it (RemoteRead) instead of running
// smartctl. One request per connection: the client writes it and closes its
// side, the server writes the response and closes.

// request asks for one drive's health, or, with no Drive, whether smartctl
// runs there at its pinned version.
type request struct {
	// Drive is the kernel's name for the drive (sda, nvme0n1).
	Drive string `json:"drive"`
}

// response answers a request: the drive's health, or why there is none. Kind
// names the error the agent would have got running smartctl itself.
type response struct {
	Health *platform.DriveHealth `json:"health,omitempty"`
	Error  string                `json:"error,omitempty"`
	Kind   string                `json:"kind,omitempty"`
}

// Kinds of error a response carries.
const (
	kindUnsupported  = "unsupported"   // platform.ErrHealthUnsupported
	kindNotPermitted = "not_permitted" // ErrNotPermitted
)

const (
	// maxRequest bounds a request (a drive name) and maxResponse a response
	// (one drive's health).
	maxRequest  = 1 << 10
	maxResponse = 64 << 10
	// socketGrace is what a request may take beyond smartctl.timeout: the
	// sidecar's own lookups and the round trip.
	socketGrace = 5 * time.Second
)

// driveName matches the kernel's names for drives (sda, nvme0n1, vdb).
var driveName = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)

// Server answers drive health requests: the sidecar's side.
type Server struct {
	Cfg config.Smartctl
	// Node finds a drive's device node here (platform.DriveNode).
	Node func(name string) (string, error)
	// Read and Version run smartctl (Read and CheckVersion).
	Read    func(context.Context, config.Smartctl, platform.Drive) (platform.DriveHealth, error)
	Version func(context.Context, config.Smartctl) error
	Log     *slog.Logger

	// busy lets one smartctl run at a time: the agent reads drives one by
	// one, and nothing should make the sidecar hammer the drives.
	busy sync.Mutex
}

// Serve answers on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Go(func() { s.handle(ctx, conn) })
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close() //nolint:errcheck // the response was written or failed already
	limit := s.Cfg.Timeout.Std() + socketGrace
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if err := conn.SetDeadline(time.Now().Add(limit)); err != nil {
		s.Log.WarnContext(ctx, "smartctl request not answered", "err", err)
		return
	}
	var req request
	res := s.answer(ctx, conn, &req)
	b, err := json.Marshal(res)
	if err == nil {
		_, err = conn.Write(b)
	}
	if err != nil {
		s.Log.WarnContext(ctx, "smartctl answer not sent", "drive", req.Drive, "err", err)
	}
}

func (s *Server) answer(ctx context.Context, conn net.Conn, req *request) response {
	b, err := io.ReadAll(io.LimitReader(conn, maxRequest+1))
	switch {
	case err != nil:
		return failure(fmt.Errorf("read the request: %w", err))
	case len(b) > maxRequest:
		return failure(errors.New("request too large"))
	}
	if err = json.Unmarshal(b, req); err != nil {
		return failure(fmt.Errorf("decode the request: %w", err))
	}
	s.busy.Lock()
	defer s.busy.Unlock()
	if req.Drive == "" {
		if err = s.Version(ctx, s.Cfg); err != nil {
			return failure(err)
		}
		return response{}
	}
	if !driveName.MatchString(req.Drive) {
		return failure(fmt.Errorf("%q is not a drive name", req.Drive))
	}
	node, err := s.Node(req.Drive)
	if err != nil {
		return failure(err)
	}
	h, err := s.Read(ctx, s.Cfg, platform.Drive{Name: req.Drive, Device: node})
	if err != nil {
		s.Log.WarnContext(ctx, "drive health not read", "drive", req.Drive, "err", err)
		return failure(err)
	}
	return response{Health: &h}
}

func failure(err error) response {
	r := response{Error: err.Error()}
	switch {
	case errors.Is(err, platform.ErrHealthUnsupported):
		r.Kind = kindUnsupported
	case errors.Is(err, ErrNotPermitted):
		r.Kind = kindNotPermitted
	}
	return r
}

// RemoteRead reads a drive's health from the sidecar at cfg.Socket.
func RemoteRead(ctx context.Context, cfg config.Smartctl, d platform.Drive) (platform.DriveHealth, error) {
	res, err := ask(ctx, cfg, request{Drive: d.Name})
	if err != nil {
		return platform.DriveHealth{}, err
	}
	if res.Health == nil {
		return platform.DriveHealth{}, errors.New("the smartctl sidecar sent no health")
	}
	return *res.Health, nil
}

// RemoteCheckVersion fails unless the sidecar at cfg.Socket answers and its
// smartctl runs at the pinned version.
func RemoteCheckVersion(ctx context.Context, cfg config.Smartctl) error {
	_, err := ask(ctx, cfg, request{})
	return err
}

func ask(ctx context.Context, cfg config.Smartctl, req request) (response, error) {
	limit := cfg.Timeout.Std() + socketGrace
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", cfg.Socket)
	if err != nil {
		return response{}, fmt.Errorf("reach the smartctl sidecar (homecloud smartctl serve) at %s: %w", cfg.Socket, err)
	}
	defer conn.Close() //nolint:errcheck // the response was read or failed already
	if deadline, ok := ctx.Deadline(); ok {
		if err = conn.SetDeadline(deadline); err != nil {
			return response{}, fmt.Errorf("smartctl sidecar: %w", err)
		}
	}
	b, err := json.Marshal(req)
	if err != nil {
		return response{}, fmt.Errorf("encode the request: %w", err)
	}
	if _, err = conn.Write(b); err != nil {
		return response{}, fmt.Errorf("smartctl sidecar: %w", err)
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return response{}, fmt.Errorf("smartctl sidecar: %T is not a Unix socket connection", conn)
	}
	if err = unix.CloseWrite(); err != nil {
		return response{}, fmt.Errorf("smartctl sidecar: %w", err)
	}
	b, err = io.ReadAll(io.LimitReader(conn, maxResponse+1))
	switch {
	case err != nil:
		return response{}, fmt.Errorf("smartctl sidecar: %w", err)
	case len(b) > maxResponse:
		return response{}, errors.New("smartctl sidecar: response too large")
	}
	var res response
	if err := json.Unmarshal(b, &res); err != nil {
		return response{}, fmt.Errorf("decode the smartctl sidecar's answer: %w", err)
	}
	switch {
	case res.Error == "":
		return res, nil
	case res.Kind == kindUnsupported:
		return response{}, sidecarError{msg: res.Error, kind: platform.ErrHealthUnsupported}
	case res.Kind == kindNotPermitted:
		return response{}, sidecarError{msg: res.Error, kind: ErrNotPermitted}
	default:
		return response{}, fmt.Errorf("smartctl sidecar: %s", res.Error)
	}
}

// sidecarError is an error the sidecar reported, worded as it wrote it (its
// message already names the kind), wrapping the error its kind names.
type sidecarError struct {
	msg  string
	kind error
}

func (e sidecarError) Error() string { return e.msg }
func (e sidecarError) Unwrap() error { return e.kind }
