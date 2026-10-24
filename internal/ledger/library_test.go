package ledger

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

const hiddenDir = `D:\Archive\Photos\Hidden\`

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
		// A Live Photo whose video arrived before its picture: nothing to display yet.
		at(rendition("M", "live_original", "live_photo_video", true), 30*time.Minute),
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

	page, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(page), []string{"L:live", "V:video", "P:photo", "M:live"}; !slices.Equal(got, want) {
		t.Fatalf("page = %v, want %v (newest first, hidden excluded)", got, want)
	}
	if page[0].Display.Version != "adjusted" || page[0].Filename == "" {
		t.Fatalf("live display = %+v filename %q, want the adjusted image", page[0].Display, page[0].Filename)
	}

	if page[3].Display != nil {
		t.Fatalf("asset without a picture yet: display = %+v, want none", page[3].Display)
	}

	first, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir}, Limit: 10, After: &first[0].Cursor})
	if err != nil || !slices.Equal(ids(rest), []string{"V:video", "P:photo", "M:live"}) {
		t.Fatalf("second page = %v, %v", ids(rest), err)
	}

	videos, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir, Kind: KindVideo}, Limit: 10})
	if err != nil || !slices.Equal(ids(videos), []string{"V:video"}) {
		t.Fatalf("videos = %v, %v", ids(videos), err)
	}
	hidden, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir, Hidden: true}, Limit: 10})
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

func TestLibraryPageByCaptureTime(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	day := time.Date(2023, 6, 20, 0, 0, 0, 0, time.UTC)
	ingest(t, db,
		// Half a second into the day: its RFC 3339 text sorts before the day's
		// whole-second start, so only an instant comparison keeps it in.
		at(rendition("A", "original", "photo", true), day.Add(500*time.Millisecond).Sub(t0)),
		at(rendition("B", "original", "photo", true), day.Add(23*time.Hour).Sub(t0)),
		at(rendition("C", "original", "photo", true), day.Add(24*time.Hour).Sub(t0)),
		at(rendition("D", "original", "photo", true), day.Add(-time.Millisecond).Sub(t0)),
		at(rendition("E", "original", "photo", true), day.AddDate(1, 0, 0).Add(time.Hour).Sub(t0)),
		at(rendition("U", "original", "photo", true), day.Add(time.Hour).Sub(t0)),
	)
	if _, err := db.w.ExecContext(ctx, `UPDATE assets SET created_at = NULL WHERE asset_id = 'U'`); err != nil {
		t.Fatal(err)
	}
	spans := []TimeSpan{{day, day.Add(24 * time.Hour)}, {day.AddDate(1, 0, 0), day.AddDate(1, 0, 1)}}

	page, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir, Taken: spans}, Limit: 10})
	if err != nil || !slices.Equal(ids(page), []string{"E:photo", "B:photo", "A:photo"}) {
		t.Fatalf("taken on the day = %v, %v; want E, B, A (C is the next day, D the day before, U unknown)", ids(page), err)
	}
	first, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir, Taken: spans}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir, Taken: spans}, Limit: 2, After: &first[1].Cursor})
	if err != nil || !slices.Equal(ids(rest), []string{"A:photo"}) {
		t.Fatalf("second page on the day = %v, %v", ids(rest), err)
	}

	before := day.Add(500 * time.Millisecond)
	page, err = db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{HiddenDir: hiddenDir, TakenBefore: &before}, Limit: 10})
	if err != nil || !slices.Equal(ids(page), []string{"D:photo"}) {
		t.Fatalf("taken before %v = %v, %v; want D only", before, ids(page), err)
	}

	taken, unknown, err := db.CaptureTimes(ctx, LibraryFilter{HiddenDir: hiddenDir})
	if err != nil || len(taken) != 5 || unknown != 1 {
		t.Fatalf("capture times = %d known, %d unknown, %v; want 5 and 1", len(taken), unknown, err)
	}
	first1, err := db.FirstCapture(ctx)
	if err != nil || first1 == nil || !first1.Equal(day.Add(-time.Millisecond)) {
		t.Fatalf("first capture = %v, %v; want D's", first1, err)
	}

	if _, err := db.LibraryPage(ctx, LibraryQuery{LibraryFilter: LibraryFilter{Taken: []TimeSpan{{day, day}}}, Limit: 1}); err == nil {
		t.Fatal("an empty span was accepted")
	}
}

func TestFirstCaptureOfAnEmptyLedger(t *testing.T) {
	db, _ := openTemp(t)
	if first, err := db.FirstCapture(context.Background()); err != nil || first != nil {
		t.Fatalf("first capture = %v, %v; want none", first, err)
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
