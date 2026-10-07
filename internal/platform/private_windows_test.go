package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestMakePrivate opens a folder to BUILTIN\Users, as a data drive's root
// does, and checks that MakePrivate closes it and the file already inside it.
func TestMakePrivate(t *testing.T) {
	dir := t.TempDir()
	user, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	open, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.String() + ")(A;OICI;FR;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := open.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "photo.heic")
	if err = os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = CheckPrivate(dir); err == nil || !strings.Contains(err.Error(), `BUILTIN\Users`) {
		t.Fatalf("CheckPrivate on a folder open to Users = %v", err)
	}

	changed, err := MakePrivate(dir)
	if err != nil || !changed {
		t.Fatalf("MakePrivate = %v, %v", changed, err)
	}
	if err = CheckPrivate(dir); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if s := sd.String(); strings.Contains(s, ";BU)") {
		t.Fatalf("the file inside still grants Users access: %s", s)
	}
	if changed, err := MakePrivate(dir); err != nil || changed {
		t.Fatalf("MakePrivate on a private folder = %v, %v; want no change", changed, err)
	}
}

func TestCheckPrivateWantsAProtectedList(t *testing.T) {
	// A temp folder inherits its parent's entries.
	if err := CheckPrivate(t.TempDir()); err == nil || !strings.Contains(err.Error(), "inherits") {
		t.Fatalf("CheckPrivate on an inheriting folder = %v", err)
	}
}
