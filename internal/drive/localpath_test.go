package drive

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestLocalName(t *testing.T) {
	lrm := string(rune(0x200E)) // an invisible mark Apple keeps in some app names
	for _, c := range []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{`a<b>c:d"e|f?g*`, "a_b_c_d_e_f_g_"},
		{"tab\there", "tab_here"},
		{"trailing. ", "trailing__"},
		{"...", "____"},
		{"", "_"},
		{"CON", "_CON"},
		{"con.txt", "_con.txt"},
		{"Com1.log", "_Com1.log"},
		{"COM¹", "_COM¹"},
		{"console.txt", "console.txt"},
		{lrm + "WhatsApp", lrm + "WhatsApp"},
	} {
		if got := localName(c.in); got != c.want {
			t.Errorf("localName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalNameShortensLongNamesDistinctly(t *testing.T) {
	a := strings.Repeat("ä", 300) + ".pdf"
	b := strings.Repeat("ä", 301) + ".pdf"
	la, lb := localName(a), localName(b)
	if la == lb || !strings.HasSuffix(la, ".pdf") || len(utf16.Encode([]rune(la))) > maxNameUnits {
		t.Fatalf("shortened names %q / %q", la[len(la)-20:], lb[len(lb)-20:])
	}
}

func file(path []string, ids ...string) File {
	return File{DriveWSID: ids[len(ids)-1], Path: path, IDs: ids}
}

func TestPlannerAssignsUniqueSafePaths(t *testing.T) {
	p := newPlanner()
	p.hold("iCloud Drive/Notes/a.txt", "FILE::z::OLD1")
	p.hold("iCloud Drive/report.pdf", "FILE::z::OLD2")
	for _, c := range []struct {
		f    File
		want string
	}{
		// Free: the safe form of its own path.
		{file([]string{"iCloud Drive", "b.txt"}, RootID, "FILE::z::B"), "iCloud Drive/b.txt"},
		// Differs only in case from another file.
		{file([]string{"iCloud Drive", "REPORT.pdf"}, RootID, "FILE::z::1A2B3C4D5E"), "iCloud Drive/REPORT (1A2B3C4D).pdf"},
		// A file named like a folder that holds other files.
		{file([]string{"iCloud Drive", "notes"}, RootID, "FILE::z::N0TES123"), "iCloud Drive/notes (N0TES123)"},
		// Files in a folder named like another file: the folder takes its own
		// id, so both land together.
		{file([]string{"iCloud Drive", "report.pdf", "x"}, RootID, "FOLDER::z::RF", "FILE::z::X1"), "iCloud Drive/report.pdf (RF)/x"},
		{file([]string{"iCloud Drive", "report.pdf", "y"}, RootID, "FOLDER::z::RF", "FILE::z::Y1"), "iCloud Drive/report.pdf (RF)/y"},
		// Taken even with the short suffix: the whole document id.
		{file([]string{"iCloud Drive", "REPORT.pdf"}, RootID, "FILE::z::1A2B3C4DZZ"), "iCloud Drive/REPORT (1A2B3C4DZZ).pdf"},
	} {
		got, err := p.assign(c.f)
		if err != nil || got != c.want {
			t.Errorf("assign(%v) = %q, %v; want %q", c.f.Path, got, err, c.want)
		}
	}
	// The suffixed name of a name at the length limit stays within it.
	long := strings.Repeat("x", maxNameUnits-4) + ".pdf"
	p.hold("iCloud Drive/"+long, "FILE::z::A")
	got, err := p.assign(file([]string{"iCloud Drive", long}, RootID, "FILE::z::BBBBBBBBB"))
	if err != nil || !strings.HasSuffix(got, " (BBBBBBBB).pdf") || len(utf16.Encode([]rune(strings.TrimPrefix(got, "iCloud Drive/")))) > maxNameUnits {
		t.Fatalf("long name = %q, %v", got, err)
	}
}
