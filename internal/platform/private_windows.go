package platform

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateSDDL grants full control to the current user, SYSTEM and
// Administrators, inherited by every file and folder inside (OICI), and
// nothing to anyone else; "P" stops the parent's entries from being
// inherited. These are the entries of a user profile folder: SYSTEM keeps
// backup and indexing tools working, and administrators could take ownership
// anyway.
func privateSDDL(user *windows.SID) string {
	return "D:P(A;OICI;FA;;;" + user.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
}

// CheckPrivate reports, as an error naming them, the accounts other than the
// current user, SYSTEM and Administrators that can access dir, and a DACL
// that still inherits its parent's entries.
func CheckPrivate(dir string) error {
	user, err := userSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", dir, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", dir, err)
	}
	if dacl == nil {
		return fmt.Errorf("%s has no access list, so everyone can access it", dir)
	}
	var others []string
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read the permissions of %s: %w", dir, err)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // the SID starts at SidStart (ACCESS_ALLOWED_ACE layout)
		if sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			continue
		}
		others = append(others, accountName(sid))
	}
	switch {
	case len(others) > 0:
		return fmt.Errorf("other accounts can access %s: %s", dir, strings.Join(others, ", "))
	case control&windows.SE_DACL_PROTECTED == 0:
		return fmt.Errorf("%s inherits the permissions of the folder holding it", dir)
	}
	return nil
}

func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil:
		return sid.String()
	case domain == "":
		return account
	}
	return domain + `\` + account
}

// MakePrivate gives dir, and everything inside it, to the current user alone
// (with SYSTEM and Administrators) when CheckPrivate finds it is not, and
// reports whether it changed anything. Windows propagates the new entries to
// every existing file and folder below (SetNamedSecurityInfo), replacing what
// they inherited; files created later inherit them.
func MakePrivate(dir string) (bool, error) {
	if CheckPrivate(dir) == nil {
		return false, nil
	}
	user, err := userSID()
	if err != nil {
		return false, err
	}
	sd, err := windows.SecurityDescriptorFromString(privateSDDL(user))
	if err != nil {
		return false, fmt.Errorf("security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("security descriptor: %w", err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return false, fmt.Errorf("make %s private: %w", dir, err)
	}
	if err := CheckPrivate(dir); err != nil {
		return true, errors.Join(errors.New("permissions were set but are still not private"), err)
	}
	return true, nil
}
