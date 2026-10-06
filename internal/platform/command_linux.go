package platform

import "os/exec"

// withoutConsole needs nothing on Linux: starting a program never opens a
// terminal window.
func withoutConsole(*exec.Cmd) {}
