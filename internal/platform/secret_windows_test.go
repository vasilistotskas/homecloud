package platform

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileAllAccess is FILE_ALL_ACCESS from winnt.h, which x/sys/windows does not define.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

// TestWriteSecretFileIsOwnerOnly checks the DACL structurally: SDDL text
// abbreviates well-known accounts (the built-in Administrator prints as "LA"),
// so string comparison is not reliable.
func TestWriteSecretFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	for _, content := range []string{"first", "second"} {
		if err := WriteSecretFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path) //nolint:gosec // test temp path
		if err != nil || string(got) != content {
			t.Fatalf("content = %q, %v; want %q", got, err, content)
		}
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Error("DACL is not protected from inheritance")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL = %v, %v; want an explicit DACL", dacl, err)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("DACL has %d entries, want 1 (%s)", dacl.AceCount, sd)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if aceErr := windows.GetAce(dacl, 0, &ace); aceErr != nil {
		t.Fatal(aceErr)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // the SID follows the ACE header in memory
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) || ace.Mask != fileAllAccess {
		t.Fatalf("ACE = type %d mask %#x sid %s; want allow FILE_ALL_ACCESS to %s",
			ace.Header.AceType, ace.Mask, sid, user.User.Sid)
	}
	if left, _ := filepath.Glob(path + ".tmp-*"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
