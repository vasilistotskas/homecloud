package auth

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// EndpointFile is the discovery file's name inside platform.StateDir.
const EndpointFile = "agent.json"

// Endpoint is how same-user clients find and authenticate to the running agent.
type Endpoint struct {
	// URL is the API base, e.g. http://127.0.0.1:8790.
	URL          string `json:"url"`
	Token        string `json:"token"`
	AgentVersion string `json:"agent_version"`
	PID          int    `json:"pid"`
}

// EndpointPath returns the discovery file's location.
func EndpointPath() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, EndpointFile), nil
}

// Publish writes e to the owner-only discovery file and returns its path.
func Publish(e Endpoint) (string, error) {
	path, err := EndpointPath()
	if err != nil {
		return "", err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return "", fmt.Errorf("create state dir: %w", mkErr)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("encode endpoint: %w", err)
	}
	if writeErr := platform.WriteSecretFile(path, data); writeErr != nil {
		return "", fmt.Errorf("publish endpoint: %w", writeErr)
	}
	return path, nil
}

// ReadEndpoint reads the discovery file written by a running agent.
func ReadEndpoint() (Endpoint, error) {
	path, err := EndpointPath()
	if err != nil {
		return Endpoint{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // fixed per-user state path
	if err != nil {
		return Endpoint{}, fmt.Errorf("read agent endpoint (is the agent running?): %w", err)
	}
	var e Endpoint
	if err := json.Unmarshal(data, &e); err != nil {
		return Endpoint{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return e, nil
}
