package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/policy"
)

// SafeToDelete is the report of iCloud Photos items whose archived copy is
// proven (policy.Judge), which the owner may delete in Apple's Photos app.
// It is read only: homecloud deletes nothing.
type SafeToDelete struct {
	// Withheld says why no item is listed even if some qualify; empty when
	// the report stands.
	Withheld string
	// Counts is the number of archived assets per verdict.
	Counts map[policy.Verdict]int
	// Items are the eligible assets, newest first; none when Withheld.
	Items []ledger.ArchivedAsset
}

// ArchivedAssets is the ledger query the report reads.
type ArchivedAssets interface {
	EachArchivedAsset(ctx context.Context, fn func(ledger.ArchivedAsset) error) error
}

// BuildSafeToDelete judges every archived asset at now. Nothing is listed
// unless the drives holding the archive were last found healthy: the archive
// would be the only copy left.
func BuildSafeToDelete(ctx context.Context, db ArchivedAssets, minAge time.Duration, storage health.Report, now time.Time) (SafeToDelete, error) {
	r := SafeToDelete{Counts: map[policy.Verdict]int{}}
	switch storage.State {
	case health.StateOK:
	case "":
		r.Withheld = "the archive drive's health has not been checked since the agent started"
	default:
		r.Withheld = fmt.Sprintf("the archive drive's health is %s: %s", storage.State, strings.Join(storage.Reasons, "; "))
	}
	addedBefore := now.Add(-minAge)
	err := db.EachArchivedAsset(ctx, func(a ledger.ArchivedAsset) error {
		v := policy.Judge(a, addedBefore)
		r.Counts[v]++
		if v == policy.Eligible && r.Withheld == "" {
			r.Items = append(r.Items, a)
		}
		return nil
	})
	if err != nil {
		return SafeToDelete{}, fmt.Errorf("safe-to-delete report: %w", err)
	}
	return r, nil
}

// Bytes is the size of the listed items' archived files: about the iCloud
// space deleting them frees, as iCloud stores the same files.
func (r SafeToDelete) Bytes() int64 {
	var n int64
	for _, a := range r.Items {
		n += a.SizeBytes()
	}
	return n
}

// MonthTotal is the listed items taken in one calendar month.
type MonthTotal struct {
	// Year and Month are zero for items iCloud gave no capture time for.
	Year   int
	Month  time.Month
	Assets int
	Bytes  int64
}

// Months totals the listed items by the month they were taken in loc,
// newest first, items without a capture time last.
func (r SafeToDelete) Months(loc *time.Location) []MonthTotal {
	byMonth := map[[2]int]*MonthTotal{}
	for _, a := range r.Items {
		var k [2]int
		if a.CreatedAt != nil {
			t := a.CreatedAt.In(loc)
			k = [2]int{t.Year(), int(t.Month())}
		}
		m, ok := byMonth[k]
		if !ok {
			m = &MonthTotal{Year: k[0], Month: time.Month(k[1])}
			byMonth[k] = m
		}
		m.Assets++
		m.Bytes += a.SizeBytes()
	}
	out := make([]MonthTotal, 0, len(byMonth))
	for _, m := range byMonth {
		out = append(out, *m)
	}
	slices.SortFunc(out, func(a, b MonthTotal) int {
		if c := cmp.Compare(b.Year, a.Year); c != 0 {
			return c
		}
		return cmp.Compare(b.Month, a.Month)
	})
	return out
}
