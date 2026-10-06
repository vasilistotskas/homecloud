package platform

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCommandRunsWithoutConsole(t *testing.T) {
	cmd := Command(context.Background(), "cmd.exe", "/c", "echo", "homecloud")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("SysProcAttr = %+v, want CREATE_NO_WINDOW", cmd.SysProcAttr)
	}
	// A console program started without a console still writes to the pipe.
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "homecloud" {
		t.Fatalf("output = %q, %v", out, err)
	}
}
