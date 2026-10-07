package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
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
					return func(ctx context.Context, c *call) error {
						if err := requireFrom(*from); err != nil {
							return err
						}
						return runIPhonePush(ctx, c, *from, *to)
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

// runIPhonePush pushes every finished backup in the from folders. A backup
// in progress, or one that changes while it is sent, is left for the next
// push; that is not an error.
func runIPhonePush(ctx context.Context, c *call, from []string, to string) error {
	key, err := pushKey()
	if err != nil {
		return err
	}
	var errs []error
	for _, dir := range from {
		backups, _, err := iphonebackup.Scan(ctx, dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(backups) == 0 {
			if err := printf(c, "%s: no backup\n", dir); err != nil {
				return err
			}
			continue
		}
		for _, b := range backups {
			line, err := pushOne(ctx, b, to, key)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if err := printf(c, "%s\n", line); err != nil {
				return err
			}
		}
	}
	return errors.Join(errs...)
}

func pushOne(ctx context.Context, b iphonebackup.Backup, to string, key iphonecopy.PushKey) (string, error) {
	name := b.DeviceName
	if name == "" {
		name = filepath.Base(b.Dir)
	}
	switch {
	case b.InProgress:
		return name + ": a backup is running; it is pushed once it finishes", nil
	case b.Err != nil:
		return "", fmt.Errorf("%s: %w", name, b.Err)
	}
	res, err := iphonecopy.Push(ctx, b, to, key)
	switch {
	case errors.Is(err, iphonecopy.ErrChanged):
		return name + ": the backup changed while it was sent; it is pushed again next time", nil
	case err != nil:
		return "", fmt.Errorf("%s: %w", name, err)
	}
	when := b.CompletedAt.Local().Format(time.DateTime)
	switch res.Outcome {
	case iphonecopy.PushArchived:
		return fmt.Sprintf("%s: the backup of %s is already archived", name, when), nil
	case iphonecopy.PushPending:
		return fmt.Sprintf("%s: the backup of %s was pushed and waits for homecloud to take it in", name, when), nil
	default:
		return fmt.Sprintf("%s: pushed the backup of %s: %d of %d files (%d MB)", name, when, res.Sent, res.Files, res.SentBytes>>20), nil
	}
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

// runSetPushKey saves the key of the archive this computer pushes to, read
// without echo from a terminal or as a line of standard input.
func runSetPushKey(c *call) error {
	var line string
	if term.IsTerminal(stdinFD()) {
		if err := printf(c, "Push key (not shown): "); err != nil {
			return err
		}
		b, err := term.ReadPassword(stdinFD())
		if err != nil {
			return fmt.Errorf("read push key: %w", err)
		}
		line = string(b)
		if err := printf(c, "\n"); err != nil {
			return err
		}
	} else {
		sc := bufio.NewScanner(c.stdin)
		if !sc.Scan() {
			return errors.Join(errors.New("no push key on standard input"), sc.Err())
		}
		line = sc.Text()
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
	return printf(c, "registered %q: pushes every %s while you are logged on, starting now\n", pushTaskName, every)
}

func runUninstallPush(ctx context.Context, c *call) error {
	if err := platform.UninstallTask(ctx, pushTaskName); err != nil {
		return fmt.Errorf("uninstall-push: %w", err)
	}
	return printf(c, "removed %q\n", pushTaskName)
}
