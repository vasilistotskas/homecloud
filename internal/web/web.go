// Package web serves homecloud's read-only local status page.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

//go:embed status.html
var templates embed.FS

var page = template.Must(template.New("status.html").
	Funcs(template.FuncMap{"deref": func(b *bool) bool { return b != nil && *b }}).
	ParseFS(templates, "status.html"))

const (
	recentRuns   = 10
	recentEvents = 20
	readTimeout  = 10 * time.Second
	writeTimeout = 30 * time.Second
	shutdownWait = 5 * time.Second
)

// Ledger is the read-only view the status page needs.
type Ledger interface {
	StateCounts(ctx context.Context) (map[ledger.AssetState]int, error)
	RecentRuns(ctx context.Context, limit int) ([]ledger.Run, error)
	RecentEvents(ctx context.Context, kinds []string, limit int) ([]ledger.Event, error)
}

type view struct {
	Now    time.Time
	States []stateCount
	Runs   []ledger.Run
	Events []ledger.Event
}

type stateCount struct {
	State ledger.AssetState
	Count int
}

// Handler returns the status page handler. allowedHost is the exact Host header
// accepted (the listen address), which defeats DNS-rebinding attacks.
func Handler(db Ledger, allowedHost string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		counts, err := db.StateCounts(r.Context())
		if err != nil {
			serverError(w, log, err)
			return
		}
		runs, err := db.RecentRuns(r.Context(), recentRuns)
		if err != nil {
			serverError(w, log, err)
			return
		}
		events, err := db.RecentEvents(r.Context(), []string{"quarantined", "rendition_changed"}, recentEvents)
		if err != nil {
			serverError(w, log, err)
			return
		}
		v := view{Now: time.Now(), Runs: runs, Events: events}
		for _, s := range []ledger.AssetState{ledger.StateVerified, ledger.StateDownloaded, ledger.StateDiscovered, ledger.StateQuarantined} {
			v.States = append(v.States, stateCount{s, counts[s]})
		}
		if err := page.Execute(w, v); err != nil {
			log.Error("render status page", "err", err)
		}
	})
	return secure(allowedHost, mux)
}

func secure(allowedHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != allowedHost {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func serverError(w http.ResponseWriter, log *slog.Logger, err error) {
	log.Error("status page query", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// Serve runs the status page on addr until ctx ends.
func Serve(ctx context.Context, addr string, db Ledger, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(db, addr, log),
		ReadHeaderTimeout: readTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		return fmt.Errorf("status page: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("status page shutdown: %w", err)
		}
		if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("status page: %w", err)
		}
		return nil
	}
}
