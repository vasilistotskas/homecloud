package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/logfile"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// pushTaskName is the scheduled task (or systemd user timer) that pushes
// iPhone backups.
const pushTaskName = "homecloud-iphone-push"

// pushFlags defines the flags of a push: where backups are and where they go.
func pushFlags(f *flags) (from *[]string, to *string) {
	to = f.requiredString("to", `the incoming folder of homecloud on the other device (its iphone_backup.incoming), e.g. \\nas\homecloud-iphone`)
	dirs := []string{}
	f.Func("from", `a folder Apple's tool backs up to, e.g. C:\Users\<you>\Apple\MobileSync\Backup (repeat for more)`, func(s string) error {
		if !filepath.IsAbs(s) {
			return fmt.Errorf("--from must be an absolute path, got %q", s)
		}
		dirs = append(dirs, s)
		return nil
	})
	return &dirs, to
}

func requireFrom(from []string) error {
	if len(from) == 0 {
		return usageError{"missing required flag --from"}
	}
	return nil
}

func iphoneCommands() []*command {
	return []*command{{
		name:  "iphone",
		usage: "send the iPhone backups made on this computer to homecloud on another device",
		subs: []*command{
			{
				name:  "push",
				usage: "send each device's finished backup that the other homecloud does not hold yet (only the files that changed)",
				define: func(f *flags) action {
					from, to := pushFlags(f)
					logged := f.Bool("log", false, "write what the push does to "+pushLogFile+" in homecloud's log folder instead of the terminal (the scheduled push on Windows has no console)")
					return func(ctx context.Context, c *call) error {
						if err := requireFrom(*from); err != nil {
							return err
						}
						if *logged {
							return runLoggedPush(ctx, *from, *to)
						}
						return runIPhonePush(ctx, *from, *to, terminalPush{c})
					}
				},
			},
			{
				name:  "install-push",
				usage: "push every --every while you are logged on (Task Scheduler / systemd user timer), and now",
				define: func(f *flags) action {
					from, to := pushFlags(f)
					every := f.Duration("every", 0, "how often to push, e.g. 1h (from 1m to 744h)")
					f.required = append(f.required, "every")
					return func(ctx context.Context, c *call) error {
						if err := requireFrom(*from); err != nil {
							return err
						}
						return runInstallPush(ctx, c, *from, *to, *every)
					}
				},
			},
			leaf("uninstall-push", "stop pushing iPhone backups on a schedule", runUninstallPush),
			{
				name:  "push-key",
				usage: "show this computer's key for pushed backups (creating it); --set saves the key of the archive on the computer that pushes",
				define: func(f *flags) action {
					set := f.Bool("set", false, "read a key (as push-key prints it on the archive's computer) and save it here")
					return func(_ context.Context, c *call) error {
						if *set {
							return runSetPushKey(c)
						}
						return runShowPushKey(c)
					}
				},
			},
		},
	}}
}

// pushOutcome is what pushing one device's backup did.
type pushOutcome int

const (
	pushRunning  pushOutcome = iota // a backup is running: pushed once it finishes
	pushChanged                     // it changed while it was sent: pushed again next time
	pushArchived                    // the archive holds it already
	pushPending                     // pushed before; the archive has not taken it in yet
	pushSent                        // pushed now
)

// pushReport is what pushing one device's backup did.
type pushReport struct {
	device    string
	completed time.Time
	outcome   pushOutcome
	// sent counts what a push sent (pushSent).
	sent iphonecopy.PushResult
}

// pushReporter tells what a push does: on the terminal (terminalPush) or in
// the push log (logPush), since the scheduled push on Windows has no console.
type pushReporter interface {
	// noBackup: a folder holds no backup.
	noBackup(ctx context.Context, dir string) error
	// done: what pushing one device's backup did.
	done(ctx context.Context, r pushReport) error
	// failed: a folder or a device's backup could not be pushed. The push
	// goes on with the others and fails at the end.
	failed(ctx context.Context, err error)
}

// runIPhonePush pushes every finished backup in the from folders, telling r
// what it does. A backup in progress, or one that changes while it is sent,
// is left for the next push; that is not an error.
func runIPhonePush(ctx context.Context, from []string, to string, r pushReporter) error {
	key, err := pushKey()
	if err != nil {
		return err
	}
	var errs []error
	fail := func(err error) {
		r.failed(ctx, err)
		errs = append(errs, err)
	}
	for _, dir := range from {
		backups, _, err := iphonebackup.Scan(ctx, dir)
		if err != nil {
			fail(err)
			continue
		}
		if len(backups) == 0 {
			if err := r.noBackup(ctx, dir); err != nil {
				return err
			}
			continue
		}
		for _, b := range backups {
			report, err := pushOne(ctx, b, to, key)
			if err != nil {
				fail(err)
				continue
			}
			if err := r.done(ctx, report); err != nil {
				return err
			}
		}
	}
	return errors.Join(errs...)
}

func pushOne(ctx context.Context, b iphonebackup.Backup, to string, key iphonecopy.PushKey) (pushReport, error) {
	r := pushReport{device: b.DeviceName, completed: b.CompletedAt}
	if r.device == "" {
		r.device = filepath.Base(b.Dir)
	}
	switch {
	case b.InProgress:
		r.outcome = pushRunning
		return r, nil
	case b.Err != nil:
		return r, fmt.Errorf("%s: %w", r.device, b.Err)
	}
	res, err := iphonecopy.Push(ctx, b, to, key)
	switch {
	case errors.Is(err, iphonecopy.ErrChanged):
		r.outcome = pushChanged
		return r, nil
	case err != nil:
		return r, fmt.Errorf("%s: %w", r.device, err)
	}
	switch res.Outcome {
	case iphonecopy.PushArchived:
		r.outcome = pushArchived
	case iphonecopy.PushPending:
		r.outcome = pushPending
	case iphonecopy.PushSent:
		r.outcome, r.sent = pushSent, res
	default:
		return r, fmt.Errorf("%s: unknown push outcome %q", r.device, res.Outcome)
	}
	return r, nil
}

// terminalPush tells a push on the terminal, a line per device; the errors
// are printed when the push ends (main).
type terminalPush struct{ c *call }

func (t terminalPush) noBackup(_ context.Context, dir string) error {
	return printf(t.c, "%s: no backup\n", dir)
}

func (t terminalPush) done(_ context.Context, r pushReport) error {
	when := r.completed.Local().Format(time.DateTime)
	switch r.outcome {
	case pushRunning:
		return printf(t.c, "%s: a backup is running; it is pushed once it finishes\n", r.device)
	case pushChanged:
		return printf(t.c, "%s: the backup changed while it was sent; it is pushed again next time\n", r.device)
	case pushArchived:
		return printf(t.c, "%s: the backup of %s is already archived\n", r.device, when)
	case pushPending:
		return printf(t.c, "%s: the backup of %s was pushed and waits for homecloud to take it in\n", r.device, when)
	case pushSent:
		return printf(t.c, "%s: pushed the backup of %s: %d of %d files (%d MB)\n",
			r.device, when, r.sent.Sent, r.sent.Files, r.sent.SentBytes>>20)
	}
	return fmt.Errorf("unknown push outcome %d", r.outcome)
}

func (terminalPush) failed(context.Context, error) {}

// pushLogFile is the logged push's log (--log), in homecloud's log folder.
const pushLogFile = "iphone-push.log"

// The push log's rotation: a push writes a few lines, so this keeps months
// of hourly pushes.
const (
	pushLogMaxBytes   = 1 << 20
	pushLogMaxBackups = 3
)

// logPush tells a push in the push log, as JSON lines like the agent's.
type logPush struct{ log *slog.Logger }

func (l logPush) noBackup(ctx context.Context, dir string) error {
	l.log.InfoContext(ctx, "no backup in this folder", "dir", dir)
	return nil
}

func (l logPush) done(ctx context.Context, r pushReport) error {
	switch r.outcome {
	case pushRunning:
		l.log.InfoContext(ctx, "backup running; pushed once it finishes", "device", r.device)
	case pushChanged:
		l.log.InfoContext(ctx, "backup changed while it was sent; pushed again next time", "device", r.device, "completed", r.completed)
	case pushArchived:
		l.log.InfoContext(ctx, "backup already archived", "device", r.device, "completed", r.completed)
	case pushPending:
		l.log.InfoContext(ctx, "backup pushed before; waits for homecloud to take it in", "device", r.device, "completed", r.completed)
	case pushSent:
		l.log.InfoContext(ctx, "backup pushed", "device", r.device, "completed", r.completed,
			"files_sent", r.sent.Sent, "files", r.sent.Files, "bytes_sent", r.sent.SentBytes)
	default:
		return fmt.Errorf("unknown push outcome %d", r.outcome)
	}
	return nil
}

func (l logPush) failed(ctx context.Context, err error) {
	l.log.ErrorContext(ctx, "backup not pushed; tried again next push", "err", err)
}

// runLoggedPush pushes as runIPhonePush does and writes what happens to the
// push log, from start to finish: the scheduled push on Windows runs without
// a console, so nothing else would keep it.
func runLoggedPush(ctx context.Context, from []string, to string) (err error) {
	dir, err := platform.LogDir()
	if err != nil {
		return err
	}
	var log *slog.Logger
	file, err := logfile.New(filepath.Join(dir, pushLogFile), logfile.Options{
		MaxBytes:   pushLogMaxBytes,
		MaxBackups: pushLogMaxBackups,
		OnError:    func(err error) { log.WarnContext(ctx, "push log not rotated", "err", err) },
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	log = slog.New(slog.NewJSONHandler(file, nil))
	started := time.Now()
	log.InfoContext(ctx, "push started", "from", from, "to", to, "version", version)
	err = runIPhonePush(ctx, from, to, logPush{log})
	attrs := []any{"ok", err == nil, "duration_ms", time.Since(started).Milliseconds()}
	if err != nil {
		log.ErrorContext(ctx, "push finished", append(attrs, "err", err)...)
	} else {
		log.InfoContext(ctx, "push finished", attrs...)
	}
	return err
}

// pushKey reads the key this computer signs pushes with.
func pushKey() (iphonecopy.PushKey, error) {
	path, err := iphonecopy.PushKeyPath()
	if err != nil {
		return iphonecopy.PushKey{}, err
	}
	key, err := iphonecopy.LoadPushKey(path)
	if errors.Is(err, iphonecopy.ErrNoPushKey) {
		return iphonecopy.PushKey{}, fmt.Errorf("%w: run \"homecloud iphone push-key\" where the archive is, then \"homecloud iphone push-key --set\" here", err)
	}
	return key, err
}

// runShowPushKey prints this computer's push key, creating it the first time.
func runShowPushKey(c *call) error {
	path, err := iphonecopy.PushKeyPath()
	if err != nil {
		return err
	}
	key, err := iphonecopy.LoadPushKey(path)
	if errors.Is(err, iphonecopy.ErrNoPushKey) {
		if key, err = iphonecopy.NewPushKey(); err != nil {
			return err
		}
		err = iphonecopy.SavePushKey(path, key)
	}
	if err != nil {
		return err
	}
	return printf(c, "iPhone push key (a secret: anything holding it can push backups here):\n%s\n\n"+
		"On the computer that pushes, run \"homecloud iphone push-key --set\" and paste it.\n", key)
}

// runSetPushKey saves the key of the archive this computer pushes to.
func runSetPushKey(c *call) error {
	line, err := readSecret(c, "Push key", "push key")
	if err != nil {
		return err
	}
	key, err := iphonecopy.ParsePushKey(line)
	if err != nil {
		return err
	}
	path, err := iphonecopy.PushKeyPath()
	if err != nil {
		return err
	}
	if err := iphonecopy.SavePushKey(path, key); err != nil {
		return err
	}
	return printf(c, "saved the push key in %s\n", path)
}

func runInstallPush(ctx context.Context, c *call, from []string, to string, every time.Duration) error {
	// A scheduled push without a key would only ever fail.
	if _, err := pushKey(); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	args := []string{"iphone", "push", "--to", to}
	for _, d := range from {
		args = append(args, "--from", d)
	}
	where := "the journal"
	if !platform.TaskOutputKept {
		args = append(args, "--log")
		dir, err := platform.LogDir()
		if err != nil {
			return err
		}
		where = filepath.Join(dir, pushLogFile)
	}
	task := platform.Task{
		Name:        pushTaskName,
		Description: "homecloud: send this computer's iPhone backups to homecloud on another device (https://github.com/vasilistotskas/homecloud)",
		Executable:  self,
		Args:        args,
		WorkingDir:  filepath.Dir(self),
		Every:       every,
	}
	if err := platform.InstallTask(ctx, task); err != nil {
		return fmt.Errorf("install-push: %w", err)
	}
	return printf(c, "registered %q: pushes every %s while you are logged on, starting now; what it does is in %s\n",
		pushTaskName, every, where)
}

func runUninstallPush(ctx context.Context, c *call) error {
	if err := platform.UninstallTask(ctx, pushTaskName); err != nil {
		return fmt.Errorf("uninstall-push: %w", err)
	}
	return printf(c, "removed %q\n", pushTaskName)
}
