package platform

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// On Windows tasks are Task Scheduler tasks started at the user's logon (a
// job's trigger repeats every Task.Every), registered through the documented
// `schtasks /Create /XML` interface using the Task Scheduler schema
// (http://schemas.microsoft.com/windows/2004/02/mit/task).

// InstallTask registers a logon task for the current user and starts it now.
func InstallTask(ctx context.Context, t Task) error {
	sid, user, err := currentUser()
	if err != nil {
		return err
	}
	doc, err := renderTask(t, sid, user)
	if err != nil {
		return err
	}
	if err := registerTask(ctx, t.Name, doc); err != nil {
		return err
	}
	return schtasks(ctx, "/Run", "/TN", t.Name)
}

// UninstallTask removes the task. A running agent keeps running until it exits.
func UninstallTask(ctx context.Context, name string) error {
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
		RestartOnFailure           *struct {
			Interval string `xml:"Interval"`
			Count    int    `xml:"Count"`
		} `xml:"RestartOnFailure,omitempty"`
	} `xml:"Settings"`
	Triggers struct {
		// triggerBaseType's Repetition precedes logonTriggerType's UserId.
		LogonTrigger struct {
			Repetition *struct {
				Interval string `xml:"Interval"`
			} `xml:"Repetition,omitempty"`
			UserID string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Actions struct {
		Context string `xml:"Context,attr"`
		Exec    struct {
			Command          string `xml:"Command"`
			Arguments        string `xml:"Arguments,omitempty"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

// renderTask returns the task definition as BOM-prefixed UTF-16LE XML, the
// encoding Task Scheduler itself uses for exported tasks.
func renderTask(task Task, userSID, userName string) ([]byte, error) {
	if err := task.validate(); err != nil {
		return nil, err
	}
	var t taskXML
	t.Version = "1.3"
	t.Xmlns = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	t.RegistrationInfo.Description = task.Description
	t.RegistrationInfo.URI = `\` + task.Name
	p := &t.Principals.Principal
	p.ID, p.UserID, p.LogonType, p.RunLevel = "Author", userSID, "InteractiveToken", "LeastPrivilege"
	st := &t.Settings
	// No limit: the agent is long-running, and a job (a first push of a
	// whole backup) may take hours; IgnoreNew skips repetitions meanwhile.
	st.ExecutionTimeLimit = "PT0S"
	st.MultipleInstancesPolicy = "IgnoreNew"
	if task.Every == 0 {
		st.RestartOnFailure = &struct {
			Interval string `xml:"Interval"`
			Count    int    `xml:"Count"`
		}{Interval: "PT5M", Count: 3}
	} else {
		// A job that fails runs again at its next repetition. No Duration:
		// the repetition goes on until logoff.
		t.Triggers.LogonTrigger.Repetition = &struct {
			Interval string `xml:"Interval"`
		}{Interval: fmt.Sprintf("PT%dM", task.Every/time.Minute)}
	}
	t.Triggers.LogonTrigger.UserID = userName
	t.Actions.Context = "Author"
	t.Actions.Exec.Command, t.Actions.Exec.WorkingDirectory = task.Executable, task.WorkingDir
	// Arguments is one command line; quote each argument the way Windows parses it.
	args := make([]string, 0, len(task.Args))
	for _, a := range task.Args {
		args = append(args, syscall.EscapeArg(a))
	}
	t.Actions.Exec.Arguments = strings.Join(args, " ")

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
	user, err := userSID()
	if err != nil {
		return "", "", err
	}
	account, domain, _, err := user.LookupAccount("")
	if err != nil {
		return "", "", fmt.Errorf("lookup account: %w", err)
	}
	return user.String(), domain + `\` + account, nil
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
	cmd := Command(ctx, "schtasks.exe", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("schtasks %s: %w: %s", args[0], err, bytes.TrimSpace(out.Bytes()))
	}
	return nil
}
