package kei

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// Outcome classifies a completed kei sync by exit code (kei src/lib.rs, v0.25.0).
type Outcome int

// Sync outcomes.
const (
	// OutcomeOK: exit 0.
	OutcomeOK Outcome = iota
	// OutcomePartial: exit 2, some assets failed; the manifest stays authoritative.
	// (Clap usage errors also exit 2, but homecloud passes fixed, tested arguments.)
	OutcomePartial
	// OutcomeAuthRequired: exit 3, 2FA or re-authentication needed (`kei login`).
	OutcomeAuthRequired
	// OutcomeAuthTerminal: exit 4, an Apple account state needs operator action.
	OutcomeAuthTerminal
)

func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomePartial:
		return "partial"
	case OutcomeAuthRequired:
		return "auth_required"
	case OutcomeAuthTerminal:
		return "auth_terminal"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// Sync runs one non-interactive `kei sync`, streaming kei's warnings and
// errors to log; what the sync did is in its report (ReadReport). At info
// level kei logs a line for every asset already on disk. Exit codes 0, 2, 3
// and 4 map to an Outcome; anything else is an error.
func Sync(ctx context.Context, cfg config.Kei, log io.Writer) (Outcome, error) {
	cmd := platform.Command(ctx, cfg.Binary,
		"sync", "--config", cfg.Config, "--no-friendly", "--no-progress-bar", "--log-level", "warn")
	// kei's log formatter (tracing-subscriber 0.3.23) writes colour codes
	// unless NO_COLOR is set, even into a file.
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.Stdout, cmd.Stderr = log, log
	err := cmd.Run()
	if err == nil {
		return OutcomeOK, nil
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("kei sync: %w", ctx.Err())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, fmt.Errorf("kei sync: %w", err)
	}
	return outcomeFor(exitErr.ExitCode())
}

// Reconcile runs `kei reconcile`, which reads kei's state and the disk without
// contacting iCloud and marks every downloaded file that is missing or
// truncated as failed, so the next sync downloads it again (kei
// src/commands/reconcile.rs, v0.25.0). It never deletes or changes files.
// Its report goes to log.
func Reconcile(ctx context.Context, cfg config.Kei, log io.Writer) error {
	cmd := platform.Command(ctx, cfg.Binary, "reconcile", "--config", cfg.Config, "--log-level", "warn")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kei reconcile: %w", err)
	}
	return nil
}

func outcomeFor(code int) (Outcome, error) {
	switch code {
	case 0:
		return OutcomeOK, nil
	case 2:
		return OutcomePartial, nil
	case 3:
		return OutcomeAuthRequired, nil
	case 4:
		return OutcomeAuthTerminal, nil
	default:
		return 0, fmt.Errorf("kei sync failed with exit code %d", code)
	}
}
