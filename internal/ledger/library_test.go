package ledger

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

const hiddenDir = `E:\HomeCloud\Photos\.Hidden\`

func at(r Rendition, offset time.Duration) Rendition {
	r.CreatedAt = t0.Add(offset)
	return r
}

func seedLibrary(t *testing.T, db *DB) {
	t.Helper()
	hidden := rendition("H", "original", "photo", true)
	hidden.LocalPath = hiddenDir + `2024\01\H.HEIC`
	ingest(t, db,
		at(rendition("P", "original", "photo", true), time.Hour),
		at(rendition("V", "original", "video", true), 2*time.Hour),
		at(rendition("L", "original", "live_photo_image", true), 3*time.Hour),
		at(rendition("L", "adjusted", "live_photo_image", true), 3*time.Hour),
		at(rendition("L", "live_original", "live_photo_video", true), 3*time.Hour),
		at(hidden, 4*time.Hour),
	)
}

func ids(page []LibraryAsset) []string {
	out := make([]string, 0, len(page))
	for _, a := range page {
		out = append(out, a.AssetID+":"+string(a.Kind))
	}
	return out
}

func TestLibraryPage(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	seedLibrary(t, db)

	page, err := db.LibraryPage(ctx, LibraryQuery{HiddenDir: hiddenDir, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(page), []string{"L:live", "V:video", "P:photo"}; !slices.Equal(got, want) {
		t.Fatalf("page = %v, want %v (newest first, hidden excluded)", got, want)
	}
	if page[0].Display.Version != "adjusted" || page[0].Filename == "" {
		t.Fatalf("live display = %+v filename %q, want the adjusted image", page[0].Display, page[0].Filename)
	}

	first, err := db.LibraryPage(ctx, LibraryQuery{HiddenDir: hiddenDir, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := db.LibraryPage(ctx, LibraryQuery{HiddenDir: hiddenDir, Limit: 10, After: &first[0].Cursor})
	if err != nil || !slices.Equal(ids(rest), []string{"V:video", "P:photo"}) {
		t.Fatalf("second page = %v, %v", ids(rest), err)
	}

	videos, err := db.LibraryPage(ctx, LibraryQuery{HiddenDir: hiddenDir, Kind: KindVideo, Limit: 10})
	if err != nil || !slices.Equal(ids(videos), []string{"V:video"}) {
		t.Fatalf("videos = %v, %v", ids(videos), err)
	}
	hidden, err := db.LibraryPage(ctx, LibraryQuery{HiddenDir: hiddenDir, Hidden: true, Limit: 10})
	if err != nil || !slices.Equal(ids(hidden), []string{"H:photo"}) {
		t.Fatalf("hidden = %v, %v", ids(hidden), err)
	}

	f, err := db.Rendition(ctx, "PrimarySync", "V", "original")
	if err != nil || f.MediaType != "video" || f.LocalPath == "" {
		t.Fatalf("Rendition = %+v, %v", f, err)
	}
	if _, err := db.Rendition(ctx, "PrimarySync", "V", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing rendition = %v", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := LibraryCursor{CreatedAt: "2024-01-02T03:04:05Z", AssetID: "A"}
	b, err := EncodeCursor(c)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeCursor(b); err != nil || got != c {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
}
