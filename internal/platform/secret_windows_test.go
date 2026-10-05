package platform

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

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
	want, err := ownerOnlySDDL()
	if err != nil {
		t.Fatal(err)
	}
	if got := sd.String(); got != want {
		t.Fatalf("DACL = %s, want %s", got, want)
	}
	if left, _ := filepath.Glob(path + ".tmp-*"); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
