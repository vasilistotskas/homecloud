package platform

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// withoutConsole runs cmd with CREATE_NO_WINDOW: a console application started
// this way gets no console window, and its standard handles are still the
// pipes exec sets up.
// https://learn.microsoft.com/en-us/windows/win32/procthread/process-creation-flags
func withoutConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
