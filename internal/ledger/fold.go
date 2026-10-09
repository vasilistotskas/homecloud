package ledger

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

// foldFunc is the SQL function fold registers: SQLite's own lower() and LIKE
// fold ASCII letters only, and names here are Greek as often as Latin.
const foldFunc = "homecloud_fold"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(foldFunc, 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch v := args[0].(type) {
			case nil:
				return "", nil
			case string:
				return fold(v), nil
			default:
				return nil, fmt.Errorf("%s: expected text, got %T", foldFunc, v)
			}
		})
}

// folder is stateless and safe to share (cases.Fold).
var folder = cases.Fold()

// fold maps s to a form in which texts that differ only in letter case,
// accents or Unicode composition are equal, as a search typed without them
// expects: it folds the case of the decomposed text (Apple stores names
// decomposed, typed text is composed), decomposes the result again (folding
// can compose or add marks) and drops the combining marks.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFD.String(folder.String(norm.NFD.String(s))))
}
