package platform

import (
	"context"
	"os/exec"
)

// Command returns a command running name with args. It is the only way
// homecloud starts programs (kei, ffmpeg, ffprobe, system tools): their output
// is captured or discarded, never shown in a terminal, and on Windows they run
// without a console window. The agent is a GUI-subsystem process
// with no console, so Windows would otherwise open a new console window for
// every console program it starts.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // callers pass a binary from validated config or a fixed system tool, and arguments never go through a shell
	withoutConsole(cmd)
	return cmd
}
