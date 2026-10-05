package kei

import "testing"

func TestVersionPattern(t *testing.T) {
	tests := []struct {
		output, want string
	}{
		{"kei 0.24.1\n", "0.24.1"},
		{"kei 0.24.1\r\n", "0.24.1"},
		{"kei 10.0.0", "10.0.0"},
		{"kei 0.24.2-dev\n", ""},
		{"not kei 0.24.1\n", ""},
	}
	for _, tt := range tests {
		m := versionPattern.FindStringSubmatch(tt.output)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != tt.want {
			t.Errorf("versionPattern(%q) = %q, want %q", tt.output, got, tt.want)
		}
	}
}
