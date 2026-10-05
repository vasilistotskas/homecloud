package platform

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

// On Windows the agent is started at logon by a Task Scheduler task, registered
// through the documented `schtasks /Create /XML` interface using the Task
// Scheduler schema (http://schemas.microsoft.com/windows/2004/02/mit/task).

// InstallAgent registers a logon task for the current user and starts it now.
func InstallAgent(ctx context.Context, svc AgentService) error {
	sid, user, err := currentUser()
	if err != nil {
		return err
	}
	doc, err := renderTask(svc, sid, user)
	if err != nil {
		return err
	}
	if err := registerTask(ctx, svc.Name, doc); err != nil {
		return err
	}
	return schtasks(ctx, "/Run", "/TN", svc.Name)
}

// UninstallAgent removes the logon task. A running agent keeps running until it exits.
func UninstallAgent(ctx context.Context, name string) error {
	return schtasks(ctx, "/Delete", "/TN", name, "/F")
}

// NotifyReady is a no-op: Task Scheduler has no readiness protocol.
func NotifyReady() error { return nil }

// NotifyStopping is a no-op: Task Scheduler has no readiness protocol.
func NotifyStopping() error { return nil }

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

// renderTask returns the task definition as BOM-prefixed UTF-16LE XML, the
// encoding Task Scheduler itself uses for exported tasks.
func renderTask(svc AgentService, userSID, userName string) ([]byte, error) {
	if !filepath.IsAbs(svc.Executable) || !filepath.IsAbs(svc.WorkingDir) {
		return nil, fmt.Errorf("agent executable and working dir must be absolute: %q, %q", svc.Executable, svc.WorkingDir)
	}
	var t taskXML
	t.Version = "1.3"
	t.Xmlns = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	t.RegistrationInfo.Description = svc.Description
	t.RegistrationInfo.URI = `\` + svc.Name
	p := &t.Principals.Principal
	p.ID, p.UserID, p.LogonType, p.RunLevel = "Author", userSID, "InteractiveToken", "LeastPrivilege"
	st := &t.Settings
	st.ExecutionTimeLimit = "PT0S" // no limit: the agent is long-running
	st.MultipleInstancesPolicy = "IgnoreNew"
	st.RestartOnFailure.Interval, st.RestartOnFailure.Count = "PT5M", 3
	t.Triggers.LogonTrigger.UserID = userName
	t.Actions.Context = "Author"
	t.Actions.Exec.Command, t.Actions.Exec.WorkingDirectory = svc.Executable, svc.WorkingDir

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

// currentUser returns the SID string and DOMAIN\name of the process owner.
func currentUser() (sid, name string, err error) {
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

func registerTask(ctx context.Context, name string, doc []byte) (err error) {
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
	return schtasks(ctx, "/Create", "/TN", name, "/XML", f.Name(), "/F")
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
