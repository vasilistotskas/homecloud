package platform

import (
	"context"
	"os/exec"
	"time"
)

// waitDelay bounds how long Wait waits, once a program has exited or its
// context ended, for output pipes a program it started still holds open, and
// for a cancelled program to exit (os/exec Cmd.WaitDelay). It never limits
// how long a program runs.
const waitDelay = 10 * time.Second

// Command returns a command running name with args. It is the only way
// homecloud starts programs (kei, ffmpeg, ffprobe, system tools): their output
// is captured or discarded, never shown in a terminal, and on Windows they run
// without a console window. The agent is a GUI-subsystem process
// with no console, so Windows would otherwise open a new console window for
// every console program it starts.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // callers pass a binary from validated config or a fixed system tool, and arguments never go through a shell
	cmd.WaitDelay = waitDelay
	withoutConsole(cmd)
	return cmd
}
