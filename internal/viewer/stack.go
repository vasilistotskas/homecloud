package viewer

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// Result is what one StackEdits found and did.
type Result struct {
	// Pairs is how many edits the ledger pairs with their original.
	Pairs int
	// Stacked were already stacked with the edit on top; Created were
	// stacked now.
	Stacked, Created int
	// NotInViewer are pairs Immich does not show both files of yet: it has
	// not scanned them, or cannot see them.
	NotInViewer int
}

// StackEdits stacks each pair's edit over its original in Immich, unless they
// already share a stack with the edit on top. photosDir is the archive's
// Photos folder on this computer and photosPath where Immich sees it.
// Repeating it changes nothing; a pair Immich has not scanned yet is stacked
// by a later call.
func StackEdits(ctx context.Context, c *Client, pairs []ledger.EditPair, photosDir, photosPath string) (Result, error) {
	res := Result{Pairs: len(pairs)}
	ids, err := c.assetIDs(ctx)
	if err != nil {
		return res, err
	}
	stacks, err := c.stacks(ctx)
	if err != nil {
		return res, err
	}
	// Each asset's stack; a stack's top asset is its primary.
	inStack := map[string]*stack{}
	for i := range stacks {
		for _, a := range stacks[i].Assets {
			inStack[a.ID] = &stacks[i]
		}
	}
	for _, p := range pairs {
		edit, err := viewerPath(p.Edit, photosDir, photosPath)
		if err != nil {
			return res, err
		}
		original, err := viewerPath(p.Original, photosDir, photosPath)
		if err != nil {
			return res, err
		}
		editID, originalID := ids[edit], ids[original]
		if editID == "" || originalID == "" {
			res.NotInViewer++
			continue
		}
		if s := inStack[editID]; s != nil && s == inStack[originalID] && s.PrimaryAssetID == editID {
			res.Stacked++
			continue
		}
		if err := c.createStack(ctx, []string{editID, originalID}); err != nil {
			return res, fmt.Errorf("stack %s over %s: %w", edit, original, err)
		}
		res.Created++
	}
	return res, nil
}

// viewerPath is where Immich sees the archive file local: its path under
// photosDir, below photosPath.
func viewerPath(local, photosDir, photosPath string) (string, error) {
	rel, err := filepath.Rel(photosDir, local)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is not in %s", local, photosDir)
	}
	return path.Join(photosPath, filepath.ToSlash(rel)), nil
}
