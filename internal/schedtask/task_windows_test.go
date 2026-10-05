//go:build windows

package schedtask

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeUTF16LE(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) < 2 || binary.LittleEndian.Uint16(b) != 0xFEFF || len(b)%2 != 0 {
		t.Fatalf("not BOM-prefixed UTF-16LE (len %d)", len(b))
	}
	units := make([]uint16, (len(b)-2)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2+2*i:])
	}
	return string(utf16.Decode(units))
}

func TestRender(t *testing.T) {
	doc, err := Render(Spec{
		Name:        "homecloud-agent",
		Description: "homecloud <agent> & friends",
		Command:     `C:\Users\me\AppData\Local\Programs\homecloud\homecloud-agent.exe`,
		WorkingDir:  `C:\Users\me\AppData\Local\Programs\homecloud`,
		UserSID:     "S-1-5-21-1-2-3-1001",
		UserName:    `HOST\me`,
	})
	if err != nil {
		t.Fatal(err)
	}
	xml := decodeUTF16LE(t, doc)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Task version="1.3" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">`,
		`<URI>\homecloud-agent</URI>`,
		`<Description>homecloud &lt;agent&gt; &amp; friends</Description>`,
		`<UserId>S-1-5-21-1-2-3-1001</UserId>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<LogonTrigger>`,
		`<UserId>HOST\me</UserId>`,
		`<Actions Context="Author">`,
		`<Command>C:\Users\me\AppData\Local\Programs\homecloud\homecloud-agent.exe</Command>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task xml missing %s\n%s", want, xml)
		}
	}
}

func TestRenderRejectsRelativePaths(t *testing.T) {
	if _, err := Render(Spec{Command: "homecloud-agent.exe", WorkingDir: `C:\x`}); err == nil {
		t.Fatal("want error for relative command")
	}
}

func TestCurrentUser(t *testing.T) {
	sid, name, err := CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sid, "S-1-") || !strings.Contains(name, `\`) {
		t.Fatalf("CurrentUser = %q, %q", sid, name)
	}
}
