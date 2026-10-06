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

func TestDisambiguate(t *testing.T) {
	for _, c := range []struct{ local, id, want string }{
		{"iCloud Drive/a.pdf", "FILE::z::1A2B3C4D5E", "iCloud Drive/a (1A2B3C4D).pdf"},
		{"iCloud Drive/README", "FILE::z::XY", "iCloud Drive/README (XY)"},
		{"iCloud Drive/.hidden", "FILE::z::QQ", "iCloud Drive/.hidden (QQ)"},
	} {
		if got := disambiguate(c.local, c.id); got != c.want {
			t.Errorf("disambiguate(%q) = %q, want %q", c.local, got, c.want)
		}
	}
}
