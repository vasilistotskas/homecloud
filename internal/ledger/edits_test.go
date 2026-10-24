package ledger

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestEditPairs(t *testing.T) {
	db, _ := openTemp(t)
	seedLibrary(t, db)
	hiddenEdit := rendition("H", "adjusted", "photo", true)
	hiddenEdit.LocalPath = hiddenDir + `2024\01\H_edited.HEIC`
	ingest(t, db,
		rendition("E", "original", "photo", true),
		rendition("E", "adjusted", "photo", true),
		// An edit whose original is not archived has nothing to pair with.
		rendition("O", "adjusted", "photo", true),
		hiddenEdit,
	)

	pairs, err := db.EditPairs(context.Background(), hiddenDir)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(pairs, func(a, b EditPair) int { return strings.Compare(a.Edit, b.Edit) })
	want := []EditPair{
		{Edit: `D:\Archive\Photos\E_adjusted`, Original: `D:\Archive\Photos\E_original`},
		{Edit: `D:\Archive\Photos\L_adjusted`, Original: `D:\Archive\Photos\L_original`},
	}
	if !slices.Equal(pairs, want) {
		t.Fatalf("pairs = %v, want %v (Hidden and unpaired edits left out)", pairs, want)
	}
}
