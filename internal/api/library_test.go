package api

import (
	"testing"
	"time"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
)

func TestOnThisDay(t *testing.T) {
	athens, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2019, 7, 1, 9, 0, 0, 0, time.UTC)

	// Europe/Athens moves its clocks forward on the last Sunday of March:
	// 2024-03-31 lasted 23 hours there.
	spans, err := onThisDay(&agentv3.CalendarDate{Year: 2026, Month: 3, Day: 31}, athens, &first)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 7 || spans[0].From.Year() != 2019 || spans[6].From.Year() != 2025 {
		t.Fatalf("spans = %v; want one per year 2019-2025", spans)
	}
	dst := spans[5]
	if !dst.From.Equal(time.Date(2024, 3, 30, 22, 0, 0, 0, time.UTC)) || dst.To.Sub(dst.From) != 23*time.Hour {
		t.Fatalf("2024-03-31 in Athens = %v to %v; want local midnight to midnight, 23 hours", dst.From, dst.To)
	}

	leap, err := onThisDay(&agentv3.CalendarDate{Year: 2026, Month: 2, Day: 29}, athens, &first)
	if err != nil || len(leap) != 2 || leap[0].From.Year() != 2020 || leap[1].From.Year() != 2024 {
		t.Fatalf("29 February = %v, %v; want 2020 and 2024 only", leap, err)
	}

	if none, noneErr := onThisDay(&agentv3.CalendarDate{Year: 2026, Month: 6, Day: 1}, athens, nil); noneErr != nil || none == nil || len(none) != 0 {
		t.Fatalf("an archive without capture times = %v, %v; want no spans", none, noneErr)
	}
	for _, d := range []*agentv3.CalendarDate{
		{Year: 2026, Month: 2, Day: 30},
		{Year: 2026, Month: 13, Day: 1},
		{Year: 2026, Month: 0, Day: 1},
		{Year: 2026, Month: 1, Day: 0},
		{Year: 0, Month: 1, Day: 1},
	} {
		if _, err := onThisDay(d, athens, &first); err == nil {
			t.Errorf("%v was accepted as a calendar date", d)
		}
	}
}

func TestCaptureMonths(t *testing.T) {
	athens, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Fatal(err)
	}
	taken := []time.Time{
		// 22:30 UTC on 31 May is already 1 June in Athens.
		time.Date(2024, 5, 31, 22, 30, 0, 0, time.UTC),
		time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2023, 12, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC),
	}
	got := captureMonths(taken, athens)
	want := [][3]int64{{2024, 6, 2}, {2024, 5, 1}, {2023, 12, 1}}
	if len(got) != len(want) {
		t.Fatalf("months = %v, want %v", got, want)
	}
	for i, m := range got {
		if [3]int64{int64(m.GetYear()), int64(m.GetMonth()), m.GetAssets()} != want[i] {
			t.Fatalf("months = %v, want %v (newest first, counted in Athens)", got, want)
		}
	}
}
