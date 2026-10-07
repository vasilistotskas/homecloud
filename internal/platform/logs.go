package platform

import "path/filepath"

// LogDir is the folder of homecloud's log files on this computer, in the
// per-user state directory: the agent's on Windows (Linux has the journal) and
// the scheduled iPhone push's.
func LogDir() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "logs"), nil
}
