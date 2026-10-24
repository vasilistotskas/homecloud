package main

import (
	"context"
	"errors"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/viewer"
)

func viewerCommands() []*command {
	return []*command{{
		name:  "viewer",
		usage: "connect the agent to the optional viewer, Immich over the archive's Photos (README, \"The viewer\")",
		subs: []*command{{
			name: "key",
			usage: "show whether the agent has the viewer's API key; --set saves one, created in Immich (Account settings, API keys) with " +
				strings.Join(viewer.Permissions, ", "),
			define: func(f *flags) action {
				set := f.Bool("set", false, "read the API key Immich showed when it was created and save it here")
				return func(_ context.Context, c *call) error {
					if *set {
						return runSetViewerKey(c)
					}
					return runShowViewerKey(c)
				}
			},
		}},
	}}
}

// runShowViewerKey says whether the key is saved, never the key itself.
func runShowViewerKey(c *call) error {
	path, err := viewer.KeyPath()
	if err != nil {
		return err
	}
	switch _, err := viewer.LoadKey(path); {
	case errors.Is(err, viewer.ErrNoKey):
		return printf(c, "No viewer API key here: run \"homecloud viewer key --set\".\n")
	case err != nil:
		return err
	}
	return printf(c, "The viewer API key is saved in %s.\n", path)
}

// runSetViewerKey saves the viewer's API key on the agent's computer.
func runSetViewerKey(c *call) error {
	line, err := readSecret(c, "Immich API key", "viewer API key")
	if err != nil {
		return err
	}
	key, err := viewer.ParseKey(line)
	if err != nil {
		return err
	}
	path, err := viewer.KeyPath()
	if err != nil {
		return err
	}
	if err := viewer.SaveKey(path, key); err != nil {
		return err
	}
	return printf(c, "saved the viewer API key in %s\n", path)
}
