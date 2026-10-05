//go:build windows

// Package schedtask registers homecloud's logon task with Windows Task Scheduler
// using the documented `schtasks /Create /XML` interface and the Task Scheduler
// schema (http://schemas.microsoft.com/windows/2004/02/mit/task).
package schedtask

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Spec describes a per-user logon task that runs one executable.
type Spec struct {
	Name        string // task path, e.g. "homecloud-agent"
	Description string
	Command     string // absolute path to the executable
	WorkingDir  string
	UserSID     string // principal (runs as this user, interactive token)
	UserName    string // DOMAIN\user for the logon trigger
}

type taskXML struct {
	XMLName          xml.Name `xml:"Task"`
	Version          string   `xml:"version,attr"`
	Xmlns            string   `xml:"xmlns,attr"`
	RegistrationInfo struct {
		Description string `xml:"Description"`
		URI         string `xml:"URI"`
	} `xml:"RegistrationInfo"`
	Principals struct {
		Principal struct {
			ID        string `xml:"id,attr"`
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		DisallowStartIfOnBatteries bool   `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     bool   `xml:"StopIfGoingOnBatteries"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		RestartOnFailure           struct {
			Interval string `xml:"Interval"`
			Count    int    `xml:"Count"`
		} `xml:"RestartOnFailure"`
	} `xml:"Settings"`
	Triggers struct {
		LogonTrigger struct {
			UserID string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Actions struct {
		Context string `xml:"Context,attr"`
		Exec    struct {
			Command          string `xml:"Command"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

// Render returns the task definition as UTF-16LE XML with a BOM, the encoding
// Task Scheduler itself uses for exported tasks.
func Render(s Spec) ([]byte, error) {
	if !filepath.IsAbs(s.Command) || !filepath.IsAbs(s.WorkingDir) {
		return nil, fmt.Errorf("task command and working dir must be absolute: %q, %q", s.Command, s.WorkingDir)
	}
	var t taskXML
	t.Version = "1.3"
	t.Xmlns = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	t.RegistrationInfo.Description = s.Description
	t.RegistrationInfo.URI = `\` + s.Name
	p := &t.Principals.Principal
	p.ID, p.UserID, p.LogonType, p.RunLevel = "Author", s.UserSID, "InteractiveToken", "LeastPrivilege"
	st := &t.Settings
	st.ExecutionTimeLimit = "PT0S" // no limit: the agent is long-running
	st.MultipleInstancesPolicy = "IgnoreNew"
	st.RestartOnFailure.Interval, st.RestartOnFailure.Count = "PT5M", 3
	t.Triggers.LogonTrigger.UserID = s.UserName
	t.Actions.Context = "Author"
	t.Actions.Exec.Command, t.Actions.Exec.WorkingDirectory = s.Command, s.WorkingDir

	body, err := xml.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode task xml: %w", err)
	}
	doc := append([]byte(`<?xml version="1.0" encoding="UTF-16"?>`+"\n"), body...)

	units := utf16.Encode([]rune(string(doc)))
	out := make([]byte, 2+2*len(units))
	binary.LittleEndian.PutUint16(out, 0xFEFF)
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2+2*i:], u)
	}
	return out, nil
}

// CurrentUser returns the SID string and DOMAIN\name of the process owner.
func CurrentUser() (sid, name string, err error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", fmt.Errorf("read token user: %w", err)
	}
	account, domain, _, err := tu.User.Sid.LookupAccount("")
	if err != nil {
		return "", "", fmt.Errorf("lookup account: %w", err)
	}
	return tu.User.Sid.String(), domain + `\` + account, nil
}

// Register creates or replaces the task.
func Register(ctx context.Context, s Spec) (err error) {
	doc, err := Render(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "homecloud-task-*.xml")
	if err != nil {
		return fmt.Errorf("create task xml: %w", err)
	}
	defer func() { err = errors.Join(err, os.Remove(f.Name())) }()
	if _, err = f.Write(doc); err != nil {
		return errors.Join(fmt.Errorf("write task xml: %w", err), f.Close())
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close task xml: %w", err)
	}
	return schtasks(ctx, "/Create", "/TN", s.Name, "/XML", f.Name(), "/F")
}

// Run starts the task now.
func Run(ctx context.Context, name string) error {
	return schtasks(ctx, "/Run", "/TN", name)
}

// Delete removes the task.
func Delete(ctx context.Context, name string) error {
	return schtasks(ctx, "/Delete", "/TN", name, "/F")
}

func schtasks(ctx context.Context, args ...string) error {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "schtasks.exe", args...) //nolint:gosec // fixed system tool; args are built by this package
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("schtasks %s: %w: %s", args[0], err, bytes.TrimSpace(out.Bytes()))
	}
	return nil
}
