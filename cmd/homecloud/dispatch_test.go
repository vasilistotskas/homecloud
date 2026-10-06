package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// testProgram is a small command tree whose actions record what they ran with.
type testProgram struct {
	ran     string
	config  string
	library string
	asset   string
}

func (tp *testProgram) program() *program {
	return &program{
		name:    "homecloud",
		summary: "test program",
		version: "1.2.3",
		root: []*command{
			leaf("doctor", "run checks", func(_ context.Context, c *call) error {
				tp.ran, tp.config = "doctor", c.config
				return nil
			}),
			leaf("fail", "always fails", func(context.Context, *call) error { return errors.New("boom") }),
			{
				name:  "quarantine",
				usage: "review quarantined assets",
				subs: []*command{{
					name:  "release",
					usage: "lift a quarantine",
					define: func(f *flags) action {
						library := f.requiredString("library", "library of the asset")
						asset := f.requiredString("asset", "asset id")
						return func(_ context.Context, c *call) error {
							tp.ran, tp.config, tp.library, tp.asset = "release", c.config, *library, *asset
							return nil
						}
					},
				}},
			},
		},
	}
}

// run executes args and returns stdout (followed by the error, if any) and
// the exit status.
func (tp *testProgram) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := tp.program().execute(t.Context(), &call{stdout: &out, stderr: &errOut}, args)
	if err != nil {
		out.WriteString("error: " + err.Error())
	}
	return out.String(), exitStatus(err)
}

func TestHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no arguments", nil, []string{"quarantine release", "--config file"}},
		{"--help", []string{"--help"}, []string{"quarantine release", "-v, --version"}},
		{"-h", []string{"-h"}, []string{"quarantine release"}},
		{"help", []string{"help"}, []string{"quarantine release"}},
		{"help for a command", []string{"help", "quarantine", "release"}, []string{"--library string", "(required)"}},
		{"--help after a command", []string{"quarantine", "release", "--help"}, []string{"Lift a quarantine", "--asset string"}},
		{"a group alone", []string{"quarantine"}, []string{"homecloud [--config file] quarantine <command>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tp testProgram
			out, status := tp.run(t, tt.args...)
			if status != 0 || tp.ran != "" {
				t.Fatalf("status %d, ran %q, output:\n%s", status, tp.ran, out)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestVersion(t *testing.T) {
	for _, flag := range []string{"--version", "-version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			var tp testProgram
			if out, status := tp.run(t, flag); status != 0 || out != "homecloud version 1.2.3\n" {
				t.Errorf("status %d, output %q", status, out)
			}
		})
	}
}

func TestUsageErrorsExitWithStatus2(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"bogus"}, `unknown command "bogus"`},
		{"unknown subcommand", []string{"quarantine", "bogus"}, `unknown command "quarantine bogus"`},
		{"missing required flag", []string{"quarantine", "release", "--library", "L"}, "missing required flag --asset"},
		{"unknown flag", []string{"doctor", "--bogus"}, "flag provided but not defined: -bogus"},
		{"unexpected argument", []string{"doctor", "extra"}, `unexpected argument "extra"`},
		{"help for no command", []string{"help", "bogus"}, `unknown command "bogus"`},
		{"help below a command", []string{"help", "doctor", "x"}, `"doctor" has no subcommands`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tp testProgram
			out, status := tp.run(t, tt.args...)
			if status != 2 || tp.ran != "" || !strings.Contains(out, tt.want) {
				t.Errorf("status %d, ran %q, output:\n%s\nwant it to contain %q", status, tp.ran, out, tt.want)
			}
		})
	}
}

func TestCommandErrorsExitWithStatus1(t *testing.T) {
	var tp testProgram
	if out, status := tp.run(t, "fail"); status != 1 || !strings.Contains(out, "boom") {
		t.Errorf("status %d, output %q", status, out)
	}
}

func TestConfigBeforeOrAfterTheCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"before", []string{"--config", "x.toml", "doctor"}, "x.toml"},
		{"after", []string{"doctor", "--config", "x.toml"}, "x.toml"},
		{"both: the later wins", []string{"--config=y.toml", "doctor", "--config", "x.toml"}, "x.toml"},
		{"neither: left to config.Resolve", []string{"doctor"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tp testProgram
			if _, status := tp.run(t, tt.args...); status != 0 || tp.ran != "doctor" || tp.config != tt.want {
				t.Errorf("status %d, ran %q with config %q, want %q", status, tp.ran, tp.config, tt.want)
			}
		})
	}
}

func TestNestedCommandGetsItsFlags(t *testing.T) {
	var tp testProgram
	_, status := tp.run(t, "--config", "c.toml", "quarantine", "release", "--library", "PrimarySync", "--asset=A1")
	if status != 0 || tp.ran != "release" || tp.library != "PrimarySync" || tp.asset != "A1" || tp.config != "c.toml" {
		t.Fatalf("status %d, %+v", status, tp)
	}
}

// The real command tree lists every command in its help.
func TestHelpListsEveryCommand(t *testing.T) {
	var out bytes.Buffer
	if err := newApp().execute(t.Context(), &call{stdout: &out}, []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"doctor", "archive init", "verify", "status", "quarantine list", "quarantine release", "open",
		"notify-test", "install", "uninstall", "icloud login", "icloud status", "drive ls",
	} {
		if !strings.Contains(out.String(), "  "+cmd+" ") {
			t.Errorf("help does not list %q:\n%s", cmd, out.String())
		}
	}
}
