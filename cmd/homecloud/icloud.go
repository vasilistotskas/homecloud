package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/vasilistotskas/homecloud/internal/drive"
	"github.com/vasilistotskas/homecloud/internal/icloud"
)

// maxCodeAttempts bounds how often a mistyped verification code may be retried.
const maxCodeAttempts = 3

func icloudCommands() []*cli.Command {
	return []*cli.Command{
		{
			Name:  "icloud",
			Usage: "homecloud's own iCloud sign-in (used for iCloud Drive)",
			Commands: []*cli.Command{
				{
					Name:   "login",
					Usage:  "sign in to iCloud in this terminal; the password is used once and never stored",
					Action: runICloudLogin,
				},
				{
					Name:   "status",
					Usage:  "check that the saved iCloud session still works",
					Action: runICloudStatus,
				},
			},
		},
		{
			Name:  "drive",
			Usage: "read iCloud Drive (read only)",
			Commands: []*cli.Command{
				{
					Name:  "ls",
					Usage: "list an iCloud Drive folder (default: the root)",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: "folder", Value: drive.RootID, Usage: "drivewsid of the folder to list"},
					},
					Action: runDriveLs,
				},
			},
		},
	}
}

// stdinFD is the terminal the owner types into.
func stdinFD() int {
	return int(os.Stdin.Fd()) //nolint:gosec // a file descriptor fits in int
}

func runICloudLogin(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	if !term.IsTerminal(stdinFD()) {
		return cli.Exit("icloud login must be run in a terminal: it asks for your Apple Account password", 1)
	}
	path, err := icloud.SessionPath()
	if err != nil {
		return err
	}
	saved, err := icloud.LoadSession(path)
	if err != nil {
		return err
	}
	// The saved session keeps its trust token, so a code may not be needed.
	c, err := icloud.New(icloud.AppleEndpoints, saved, time.Now)
	if err != nil {
		return err
	}

	if err = printf(cmd, "Apple Account: %s\nPassword (not shown, never stored): ", cfg.ICloud.AppleID); err != nil {
		return err
	}
	password, err := term.ReadPassword(stdinFD())
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if err = printf(cmd, "\n"); err != nil {
		return err
	}

	l, needsCode, err := c.StartLogin(ctx, cfg.ICloud.AppleID, string(password))
	if err != nil {
		return err
	}
	l.AwaitingApproval = func() {
		_ = printf(cmd, "Your account uses Advanced Data Protection: approve iCloud Drive access on one of your Apple devices (waiting up to 5 minutes)…\n") //nolint:errcheck // a status line
	}
	if needsCode {
		if err := enterCode(ctx, cmd, l); err != nil {
			return err
		}
	}
	if err := icloud.SaveSession(path, c.Session()); err != nil {
		return err
	}
	return printf(cmd, "Signed in. The session is saved for homecloud (owner only); your password was not stored.\n")
}

// enterCode reads the verification code, offering text messages instead of
// the trusted devices.
func enterCode(ctx context.Context, cmd *cli.Command, l *icloud.Login) error {
	if err := printf(cmd, "A verification code was sent to your trusted Apple devices.\n"); err != nil {
		return err
	}
	for i, p := range l.Phones {
		if err := printf(cmd, "  or type \"sms %d\" to get it by text at %s\n", i+1, p.Number); err != nil {
			return err
		}
	}
	in := bufio.NewScanner(cmd.Root().Reader)
	var phone *icloud.Phone
	for attempt := 0; attempt < maxCodeAttempts; {
		if err := printf(cmd, "Code: "); err != nil {
			return err
		}
		if !in.Scan() {
			if err := in.Err(); err != nil {
				return fmt.Errorf("read code: %w", err)
			}
			return errors.New("no verification code entered")
		}
		line := strings.TrimSpace(in.Text())
		if n, ok := strings.CutPrefix(line, "sms "); ok {
			i, err := strconv.Atoi(n)
			if err != nil || i < 1 || i > len(l.Phones) {
				if err := printf(cmd, "No such number.\n"); err != nil {
					return err
				}
				continue
			}
			phone = &l.Phones[i-1]
			if err := l.SendText(ctx, *phone); err != nil {
				return err
			}
			if err := printf(cmd, "A code was texted to %s.\n", phone.Number); err != nil {
				return err
			}
			continue
		}
		err := l.SubmitCode(ctx, line, phone)
		if !errors.Is(err, icloud.ErrCodeRejected) {
			return err
		}
		attempt++
		if err := printf(cmd, "That code was not accepted.\n"); err != nil {
			return err
		}
	}
	return icloud.ErrCodeRejected
}

// openSession restores the saved session and makes sure it still works.
func openSession(ctx context.Context) (*icloud.Client, *icloud.SessionFile, error) {
	path, err := icloud.SessionPath()
	if err != nil {
		return nil, nil, err
	}
	file, saved, err := icloud.OpenSessionFile(path)
	if err != nil {
		return nil, nil, err
	}
	c, err := icloud.New(icloud.AppleEndpoints, saved, time.Now)
	if err != nil {
		return nil, nil, err
	}
	if err := c.Refresh(ctx); err != nil {
		if errors.Is(err, icloud.ErrSignInRequired) {
			return nil, nil, cli.Exit("iCloud sign-in required: run \"homecloud icloud login\"", 1)
		}
		return nil, nil, err
	}
	return c, file, nil
}

// keepSession saves what the session learned, unless a newer sign-in was
// saved meanwhile (the agent may be syncing).
func keepSession(c *icloud.Client, file *icloud.SessionFile) error {
	_, err := file.Save(c.Session())
	return err
}

func runICloudStatus(ctx context.Context, cmd *cli.Command) error {
	c, file, err := openSession(ctx)
	if err != nil {
		return err
	}
	s := c.Session()
	if err := keepSession(c, file); err != nil {
		return err
	}
	_, driveErr := c.Service(icloud.ServiceDrive)
	adp := "no"
	if c.NeedsDeviceApproval() {
		adp = "yes"
	}
	return printf(cmd, "Signed in as %s since %s\niCloud Drive reachable: %t\nAdvanced Data Protection: %s\n",
		s.AppleID, s.SignedInAt.Local().Format(time.DateTime), driveErr == nil, adp)
}

func runDriveLs(ctx context.Context, cmd *cli.Command) error {
	c, file, err := openSession(ctx)
	if err != nil {
		return err
	}
	folders, err := drive.Folders(ctx, c, cmd.String("folder"))
	if err != nil {
		return err
	}
	if err := keepSession(c, file); err != nil {
		return err
	}
	for _, it := range folders[0].Items {
		modified := ""
		if !it.DateModified.IsZero() {
			modified = it.DateModified.Local().Format(time.DateTime)
		}
		if err := printf(cmd, "%-13s %12d  %-19s  %s\t%s\n", it.Type, it.Size, modified, it.FullName(), it.DriveWSID); err != nil {
			return err
		}
	}
	return printf(cmd, "%d items\n", len(folders[0].Items))
}
