package tool

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The test binary doubles as a fake external tool: when HOMECLOUD_FAKE_TOOL_OUTPUT
// is set it prints that value and exits, standing in for kei/ffprobe.
func TestMain(m *testing.M) {
	if out, ok := os.LookupEnv("HOMECLOUD_FAKE_TOOL_OUTPUT"); ok {
		fmt.Print(out)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakePin(t *testing.T, output, want string) Pin {
	t.Helper()
	t.Setenv("HOMECLOUD_FAKE_TOOL_OUTPUT", output)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Pin{
		Name:    "fake",
		Binary:  exe,
		Pattern: regexp.MustCompile(`(?m)^fake (\d+\.\d+\.\d+)\s*$`),
		Want:    want,
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name, output, want, wantErr string
	}{
		{"exact match", "fake 1.2.3\n", "1.2.3", ""},
		{"different version", "fake 1.2.4\n", "1.2.3", "version 1.2.4 found, 1.2.3 required"},
		{"unparseable", "something else\n", "1.2.3", "unrecognised version output"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fakePin(t, tt.output, tt.want).Check(context.Background())
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestVersionMissingBinary(t *testing.T) {
	p := Pin{Name: "missing", Binary: `C:\definitely\not\here.exe`, Pattern: regexp.MustCompile(`(.*)`)}
	if _, err := p.Version(context.Background()); err == nil {
		t.Fatal("want error for missing binary")
	}
}
