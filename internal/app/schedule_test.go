package app

import (
	"testing"
	"time"
)

func TestNextRunAt(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 6, h, m, 0, 0, time.UTC) }
	tests := []struct {
		name      string
		last      time.Time
		every     time.Duration
		nextCycle time.Time
		want      time.Time
	}{
		{"unknown while a cycle runs", at(0, 0), time.Hour, time.Time{}, time.Time{}},
		{"never completed: next cycle", time.Time{}, 24 * time.Hour, at(5, 0), at(5, 0)},
		{"already due: next cycle", at(0, 0), 2 * time.Hour, at(5, 0), at(5, 0)},
		{"due between cycles: the cycle after", at(0, 30), 6 * time.Hour, at(5, 0), at(7, 0)},
		{"due on a cycle: that cycle", at(0, 0), 6 * time.Hour, at(5, 0), at(6, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextRunAt(tt.last, tt.every, tt.nextCycle, time.Hour); !got.Equal(tt.want) {
				t.Fatalf("NextRunAt = %v, want %v", got, tt.want)
			}
		})
	}
}
