package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/vasilistotskas/homecloud/internal/config"
)

// command is one node of the command tree: a group of subcommands, or a leaf
// that defines its flags and returns the action that reads them.
type command struct {
	name  string
	usage string
	subs  []*command
	// define registers a leaf's flags and returns its action; nil for a group.
	define func(f *flags) action
}

// action runs a leaf command.
type action func(ctx context.Context, c *call) error

// call is what an action runs with: its input and output, and the --config
// flag ("" lets config.Resolve fall back to HOMECLOUD_CONFIG, then the
// per-user default).
type call struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	config string
}

// flags is a leaf's flag set and which of its flags must be given.
type flags struct {
	*flag.FlagSet
	required []string
}

// requiredString defines a string flag that must be given.
func (f *flags) requiredString(name, usage string) *string {
	f.required = append(f.required, name)
	return f.String(name, "", usage)
}

// usageError is a mistake in the command line: exit status 2, as package
// flag uses for bad flags.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// exitStatus is the process exit status for the error a command line ended
// with.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	if _, usage := errors.AsType[usageError](err); usage {
		return 2
	}
	return 1
}

// program is the command line: its name, version and command tree.
type program struct {
	name    string
	summary string
	version string
	root    []*command
}

// configUsage describes --config; the back-quoted word names its value in
// help (flag.UnquoteUsage).
const configUsage = "the homecloud TOML config `file` (default: $" + config.EnvPath + ", then the per-user config directory)"

// execute runs the command line args (without the program name). Help and
// the version go to stdout; usage errors are returned as usageError.
func (a *program) execute(ctx context.Context, c *call, args []string) error {
	global := flag.NewFlagSet(a.name, flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&c.config, "config", "", configUsage)
	showVersion := global.Bool("version", false, "print the version")
	global.BoolVar(showVersion, "v", false, "print the version")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return a.printHelp(c.stdout, nil, a.root)
		}
		return usageError{fmt.Sprintf("%v (see %s --help)", err, a.name)}
	}
	if *showVersion {
		_, err := fmt.Fprintf(c.stdout, "%s version %s\n", a.name, a.version)
		return writeErr(err)
	}
	rest := global.Args()
	if len(rest) == 0 {
		return a.printHelp(c.stdout, nil, a.root)
	}
	if rest[0] == "help" {
		return a.help(c.stdout, rest[1:])
	}

	var path []string
	level := a.root
	for {
		cmd := find(level, rest[0])
		if cmd == nil {
			return usageError{fmt.Sprintf("unknown command %q (see %s --help)", strings.Join(append(path, rest[0]), " "), a.name)}
		}
		path, rest = append(path, cmd.name), rest[1:]
		if cmd.define != nil {
			return a.runLeaf(ctx, c, path, cmd, rest)
		}
		if len(rest) == 0 || rest[0] == "-h" || rest[0] == "--help" || rest[0] == "-help" {
			return a.printHelp(c.stdout, path, cmd.subs)
		}
		level = cmd.subs
	}
}

// leafFlags is a leaf's flag set, including --config (bound to config) so it
// may also follow the command, and the leaf's action.
func (a *program) leafFlags(path []string, cmd *command, config *string) (*flags, action) {
	f := &flags{FlagSet: flag.NewFlagSet(a.name+" "+strings.Join(path, " "), flag.ContinueOnError)}
	f.SetOutput(io.Discard)
	f.StringVar(config, "config", *config, configUsage)
	return f, cmd.define(f)
}

// runLeaf parses a leaf's flags, checks the required ones and runs it.
func (a *program) runLeaf(ctx context.Context, c *call, path []string, cmd *command, args []string) error {
	f, run := a.leafFlags(path, cmd, &c.config)
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return a.printLeafHelp(c.stdout, path, cmd, f)
		}
		return usageError{fmt.Sprintf("%s: %v (see %s --help)", strings.Join(path, " "), err, f.Name())}
	}
	if f.NArg() > 0 {
		return usageError{fmt.Sprintf("%s: unexpected argument %q (see %s --help)", strings.Join(path, " "), f.Arg(0), f.Name())}
	}
	given := map[string]bool{}
	f.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	for _, name := range f.required {
		if !given[name] {
			return usageError{fmt.Sprintf("%s: missing required flag --%s (see %s --help)", strings.Join(path, " "), name, f.Name())}
		}
	}
	return run(ctx, c)
}

// help prints the help of the command at path ("help quarantine release").
func (a *program) help(w io.Writer, path []string) error {
	level := a.root
	for i, name := range path {
		cmd := find(level, name)
		if cmd == nil {
			return usageError{fmt.Sprintf("unknown command %q (see %s --help)", strings.Join(path[:i+1], " "), a.name)}
		}
		if cmd.define != nil {
			if i != len(path)-1 {
				return usageError{fmt.Sprintf("%q has no subcommands (see %s --help)", strings.Join(path[:i+1], " "), a.name)}
			}
			var config string
			f, _ := a.leafFlags(path, cmd, &config)
			return a.printLeafHelp(w, path, cmd, f)
		}
		level = cmd.subs
	}
	return a.printHelp(w, path, level)
}

// row is one line of a help table: a name and what it does.
type row struct{ name, text string }

// table aligns rows in two columns, indented under a heading.
func table(rows []row) ([]byte, error) {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "  %s\t%s\n", r.name, r.text); err != nil {
			return nil, writeErr(err)
		}
	}
	if err := tw.Flush(); err != nil {
		return nil, writeErr(err)
	}
	return b.Bytes(), nil
}

// leaves lists every leaf under cmds as "group command" rows.
func leaves(prefix string, cmds []*command) []row {
	var rows []row
	for _, cmd := range cmds {
		name := strings.TrimSpace(prefix + " " + cmd.name)
		if cmd.define != nil {
			rows = append(rows, row{name, cmd.usage})
			continue
		}
		rows = append(rows, leaves(name, cmd.subs)...)
	}
	return rows
}

// printHelp lists the commands under path, every leaf on its own line.
func (a *program) printHelp(w io.Writer, path []string, level []*command) error {
	commands, err := table(leaves(strings.Join(path, " "), level))
	if err != nil {
		return err
	}
	global, err := table([]row{
		{"--config file", strings.ReplaceAll(configUsage, "`", "")},
		{"-v, --version", "print the version"},
		{"-h, --help", "show help"},
	})
	if err != nil {
		return err
	}
	var b []byte
	if len(path) == 0 {
		b = fmt.Appendf(b, "%s: %s\n\n", a.name, a.summary)
	}
	usage := slices.Concat([]string{a.name, "[--config file]"}, path, []string{"<command> [flags]"})
	b = fmt.Appendf(b, "Usage:\n  %s\n\nCommands:\n%s\nGlobal flags:\n%s\nRun \"%s <command> --help\" for a command's flags.\n",
		strings.Join(usage, " "), commands, global, a.name)
	_, err = w.Write(b)
	return writeErr(err)
}

// printLeafHelp describes one command and its flags.
func (a *program) printLeafHelp(w io.Writer, path []string, cmd *command, f *flags) error {
	var rows []row
	f.VisitAll(func(fl *flag.Flag) {
		kind, usage := flag.UnquoteUsage(fl)
		name := "--" + fl.Name
		if kind != "" {
			name += " " + kind
		}
		switch {
		case slices.Contains(f.required, fl.Name):
			usage += " (required)"
		case fl.DefValue != "" && kind != "":
			usage += fmt.Sprintf(" (default %q)", fl.DefValue)
		}
		rows = append(rows, row{name, usage})
	})
	flagRows, err := table(rows)
	if err != nil {
		return err
	}
	b := fmt.Appendf(nil, "Usage:\n  %s %s [flags]\n\n%s\n\nFlags:\n%s", a.name, strings.Join(path, " "), upperFirst(cmd.usage), flagRows)
	_, err = w.Write(b)
	return writeErr(err)
}

func find(cmds []*command, name string) *command {
	for _, cmd := range cmds {
		if cmd.name == name {
			return cmd
		}
	}
	return nil
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func writeErr(err error) error {
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
