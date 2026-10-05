package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// StateDir is the per-user directory for homecloud's runtime state:
// %LOCALAPPDATA%\homecloud.
func StateDir() (string, error) {
	dir, err := os.UserCacheDir() // %LocalAppData% on Windows
	if err != nil {
		return "", fmt.Errorf("locate local app data: %w", err)
	}
	return filepath.Join(dir, "homecloud"), nil
}

// ownerOnlySDDL grants the current user full control and nobody else anything;
// "P" protects the DACL from inheriting the parent folder's entries.
func ownerOnlySDDL() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	return "D:P(A;;FA;;;" + user.User.Sid.String() + ")", nil
}

// WriteSecretFile atomically replaces path with data. The file is created with
// an owner-only DACL, so it is never readable by others, even briefly.
func WriteSecretFile(path string, data []byte) (err error) {
	sddl, err := ownerOnlySDDL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa)) //nolint:gosec // fixed-size Win32 struct

	var suffix [8]byte
	if _, randErr := rand.Read(suffix[:]); randErr != nil {
		return fmt.Errorf("temp name: %w", randErr)
	}
	tmp := path + ".tmp-" + hex.EncodeToString(suffix[:])
	name, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return fmt.Errorf("encode path %q: %w", tmp, err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("create %q: %w", tmp, err)
	}
	f := os.NewFile(uintptr(h), tmp)
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp))
		}
	}()
	if _, err = f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write %q: %w", tmp, err), f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync %q: %w", tmp, err), f.Close())
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmp, err)
	}
	// Renaming within a volume keeps the file's own security descriptor.
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
