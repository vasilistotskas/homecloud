// Package ui embeds the built web app (Vite output in dist/), which the agent
// serves. Build it first: `pnpm install && pnpm build` in this directory.
package ui

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built web app, rooted at its index.html.
func Assets() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("web app assets: %w", err)
	}
	return sub, nil
}
