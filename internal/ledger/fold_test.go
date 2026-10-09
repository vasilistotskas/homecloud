package ledger

import (
	"context"
	"testing"
)

func TestFoldIgnoresCaseAccentsAndComposition(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"IMG_0001.HEIC", "img_0001.heic"},
		{"ΚΑΛΟΚΑΊΡΙ", "καλοκαιρι"},     // case and tonos
		{"Οδυσσεύς", "ΟΔΥΣΣΕΥΣ"},       // final sigma folds like any sigma
		{"Ελλάδα", "Ελλα\u0301δα"},     // composed and decomposed ά
		{"Straße", "STRASSE"},          // full case folding
		{"Café.pdf", "cafe\u0301.PDF"}, // Apple's decomposed names
		{"İstanbul", "istanbul"},       // a mark folding adds is dropped too
	} {
		if fold(tc.a) != fold(tc.b) {
			t.Errorf("fold(%q) = %q, fold(%q) = %q; want equal", tc.a, fold(tc.a), tc.b, fold(tc.b))
		}
	}
	if fold("Σύμβαση") == fold("Σύμβολο") {
		t.Error("different words fold alike")
	}
}

func TestEventsSearchSubjects(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("P", "original", "photo", true), rendition("Q", "original", "photo", true))
	if err := db.RecordDriveFile(ctx, driveFile("FILE::z::1", "Έγγραφα/Συμβόλαιο.pdf"), "", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveIPhoneBackup(ctx, IPhoneBackupRecord{
		Folder: `C:\Backup\00008110`, UDID: "00008110", DeviceName: "Βασίλης iPhone", State: "ok",
	}, nil, t0); err != nil {
		t.Fatal(err)
	}
	all := []string{EventStateChanged, EventDriveFileArchived, EventIPhoneBackupChanged}

	search := func(text string) []string {
		t.Helper()
		events, err := db.Events(ctx, EventQuery{Kinds: all, Text: text}, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		var subjects []string
		for _, e := range events {
			subjects = append(subjects, e.Kind+":"+e.Asset)
		}
		return subjects
	}

	if got := search("p_ORIGINAL"); len(got) != 1 || got[0] != EventStateChanged+":P" {
		t.Errorf("photo file name: %v", got)
	}
	if got := search("εγγραφα/συμβολαιο"); len(got) != 1 || got[0] != EventDriveFileArchived+":FILE::z::1" {
		t.Errorf("Drive path typed without accents: %v", got)
	}
	if got := search("ΒΑΣΙΛΗΣ"); len(got) != 1 || got[0] != EventIPhoneBackupChanged+":" {
		t.Errorf("iPhone name: %v", got)
	}
	// Detail values that are not the subject do not match.
	if got := search("downloaded"); len(got) != 0 {
		t.Errorf("a state name matched: %v", got)
	}
	if got := search("00008110"); len(got) != 0 {
		t.Errorf("a device's UDID matched: %v", got)
	}
	if got := search(""); len(got) != 4 {
		t.Errorf("empty query = %v, want every event", got)
	}
}
