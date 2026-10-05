package kei

import "testing"

func TestOutcomeFor(t *testing.T) {
	tests := []struct {
		code    int
		want    Outcome
		wantErr bool
	}{
		{0, OutcomeOK, false},
		{2, OutcomePartial, false},
		{3, OutcomeAuthRequired, false},
		{4, OutcomeAuthTerminal, false},
		{1, 0, true},
		{130, 0, true},
	}
	for _, tt := range tests {
		got, err := outcomeFor(tt.code)
		if (err != nil) != tt.wantErr || (!tt.wantErr && got != tt.want) {
			t.Errorf("outcomeFor(%d) = %v, %v; want %v, err=%v", tt.code, got, err, tt.want, tt.wantErr)
		}
	}
}

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
