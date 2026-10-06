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

	"golang.org/x/term"

	"github.com/vasilistotskas/homecloud/internal/drive"
	"github.com/vasilistotskas/homecloud/internal/icloud"
)

// maxCodeAttempts bounds the codes entered and the codes asked for again (by
// text or on the devices) in one sign-in; Apple locks out repeated attempts.
const maxCodeAttempts = 3

func icloudCommands() []*command {
	return []*command{
		{
			name:  "icloud",
			usage: "homecloud's own iCloud sign-in (used for iCloud Drive)",
			subs: []*command{
				leaf("login", "sign in to iCloud in this terminal; the password is used once and never stored", runICloudLogin),
				leaf("status", "check that the saved iCloud session still works", runICloudStatus),
			},
		},
		{
			name:  "drive",
			usage: "read iCloud Drive (read only)",
			subs: []*command{{
				name:  "ls",
				usage: "list an iCloud Drive folder (default: the root)",
				define: func(f *flags) action {
					folder := f.String("folder", drive.RootID, "drivewsid of the folder to list")
					return func(ctx context.Context, c *call) error { return runDriveLs(ctx, c, *folder) }
				},
			}},
		},
	}
}

// stdinFD is the terminal the owner types into.
func stdinFD() int {
	return int(os.Stdin.Fd()) //nolint:gosec // a file descriptor fits in int
}

func runICloudLogin(ctx context.Context, c *call) error {
	cfg, err := loadConfig(c)
	if err != nil {
		return err
	}
	if !term.IsTerminal(stdinFD()) {
		return errors.New("icloud login must be run in a terminal: it asks for your Apple Account password")
	}
	path, err := icloud.SessionPath()
	if err != nil {
		return err
	}
	// A fresh session that keeps only the trust token of the same Apple
	// Account, so a code may not be needed.
	client, err := icloud.New(icloud.AppleEndpoints, icloud.LoginSession(path, cfg.ICloud.AppleID), cfg.ICloud.RequestTimeout.Std(), time.Now)
	if err != nil {
		return err
	}

	if err = printf(c, "Apple Account: %s\nPassword (not shown, never stored): ", cfg.ICloud.AppleID); err != nil {
		return err
	}
	password, err := term.ReadPassword(stdinFD())
	defer clear(password)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if err = printf(c, "\n"); err != nil {
		return err
	}

	l, needsCode, err := client.StartLogin(ctx, icloud.LoginRequest{
		AppleID: cfg.ICloud.AppleID, Password: password,
		AwaitingApproval: func() {
			_ = printf(c, "Your account uses Advanced Data Protection: approve iCloud Drive access on one of your Apple devices (waiting up to %s)…\n", icloud.ApprovalWait) //nolint:errcheck // a status line
		},
	})
	if err != nil {
		return err
	}
	defer l.Close()
	if needsCode {
		if err := enterCode(ctx, c, l); err != nil {
			return err
		}
	}
	if err := icloud.SaveSession(path, client.Session()); err != nil {
		return err
	}
	return printf(c, "Signed in. The session is saved for homecloud (owner only); your password was not stored.\n")
}

// enterCode reads the verification code. "sms N" asks for it by text instead
// and "device" on the trusted devices again; both count, like wrong codes,
// against maxCodeAttempts.
func enterCode(ctx context.Context, c *call, l *icloud.Login) error {
	if err := printf(c, "A verification code was sent to your trusted Apple devices.\n"); err != nil {
		return err
	}
	for i, p := range l.Phones {
		if err := printf(c, "  or type \"sms %d\" to get it by text at %s\n", i+1, p.Number); err != nil {
			return err
		}
	}
	in := bufio.NewScanner(c.stdin)
	var phone *icloud.Phone // nil: the code comes from the trusted devices
	codes, requests := 0, 0
	for codes < maxCodeAttempts {
		if err := printf(c, "Code: "); err != nil {
			return err
		}
		if !in.Scan() {
			if err := in.Err(); err != nil {
				return fmt.Errorf("read code: %w", err)
			}
			return errors.New("no verification code entered")
		}
		line := strings.TrimSpace(in.Text())
		n, isText := strings.CutPrefix(line, "sms ")
		if isText || line == "device" {
			if requests++; requests > maxCodeAttempts {
				return errors.New("too many code requests; run icloud login again later")
			}
			next, err := requestCode(ctx, c, l, isText, n)
			if err != nil {
				return err
			}
			if next != nil || !isText {
				phone = next
			}
			continue
		}
		err := l.SubmitCode(ctx, line, phone)
		if !errors.Is(err, icloud.ErrCodeRejected) {
			return err
		}
		codes++
		if err := printf(c, "That code was not accepted.\n"); err != nil {
			return err
		}
	}
	return icloud.ErrCodeRejected
}

// requestCode asks for a new code by text to phone number n ("sms n") or on
// the trusted devices, and returns the phone the next code comes to (nil: the
// devices, or no change when n is not a listed number).
func requestCode(ctx context.Context, c *call, l *icloud.Login, isText bool, n string) (*icloud.Phone, error) {
	if !isText {
		if err := l.RequestDeviceCode(ctx); err != nil {
			return nil, err
		}
		return nil, printf(c, "A new code was sent to your trusted Apple devices.\n")
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 1 || i > len(l.Phones) {
		return nil, printf(c, "No such number.\n")
	}
	phone := &l.Phones[i-1]
	if err := l.SendText(ctx, *phone); err != nil {
		return nil, err
	}
	return phone, printf(c, "A code was texted to %s.\n", phone.Number)
}

// openSession restores the saved session and makes sure it still works.
func openSession(ctx context.Context, c *call) (*icloud.Client, *icloud.SessionFile, error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return nil, nil, err
	}
	path, err := icloud.SessionPath()
	if err != nil {
		return nil, nil, err
	}
	file, saved, err := icloud.OpenSessionFile(path)
	if err != nil {
		return nil, nil, err
	}
	client, err := icloud.New(icloud.AppleEndpoints, saved, cfg.ICloud.RequestTimeout.Std(), time.Now)
	if err != nil {
		return nil, nil, err
	}
	if err := client.Refresh(ctx); err != nil {
		if errors.Is(err, icloud.ErrSignInRequired) {
			return nil, nil, errors.New("iCloud sign-in required: run \"homecloud icloud login\"")
		}
		return nil, nil, err
	}
	return client, file, nil
}

// keepSession saves what the session learned, unless a newer sign-in was
// saved meanwhile (the agent may be syncing).
func keepSession(c *icloud.Client, file *icloud.SessionFile) error {
	_, err := file.Save(c.Session())
	return err
}

func runICloudStatus(ctx context.Context, c *call) error {
	client, file, err := openSession(ctx, c)
	if err != nil {
		return err
	}
	// A real call: the Drive root lists only when the session and, under
	// Advanced Data Protection, the owner's approval still work.
	folders, driveErr := drive.Folders(ctx, client, drive.RootID)
	if err := keepSession(client, file); err != nil {
		return err
	}
	s := client.Session()
	adp := "no"
	if client.NeedsDeviceApproval() {
		adp = "yes"
	}
	var reachable string
	if driveErr != nil {
		reachable = "no: " + driveErr.Error()
	} else {
		reachable = fmt.Sprintf("yes (%d items at the top)", len(folders[0].Items))
	}
	return printf(c, "Signed in as %s since %s\niCloud Drive reachable: %s\nAdvanced Data Protection: %s\n",
		s.AppleID, s.SignedInAt.Local().Format(time.DateTime), reachable, adp)
}

func runDriveLs(ctx context.Context, c *call, folder string) error {
	client, file, err := openSession(ctx, c)
	if err != nil {
		return err
	}
	folders, err := drive.Folders(ctx, client, folder)
	if err != nil {
		return err
	}
	if err := keepSession(client, file); err != nil {
		return err
	}
	for _, it := range folders[0].Items {
		modified := ""
		if !it.DateModified.IsZero() {
			modified = it.DateModified.Local().Format(time.DateTime)
		}
		if err := printf(c, "%-13s %12d  %-19s  %s\t%s\n", it.Type, it.Size, modified, it.FullName(), it.DriveWSID); err != nil {
			return err
		}
	}
	return printf(c, "%d items\n", len(folders[0].Items))
}
