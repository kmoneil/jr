//go:build windows

package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/kmoneil/jr/internal/errs"
)

// A file on Windows has no mode to speak of. Go reports every regular file as
// 0666, or 0444 when it is read-only, so the Unix check refused the store on
// every read after the first `jr auth login`, with a remedy naming chmod. What
// decides who can open a file there is its discretionary ACL, and that is what
// these read and write.

// storeSDDL is the DACL the store is written with: protected, so nothing is
// inherited from the directory, and one entry granting the current user full
// control. It is 0600 in the only terms Windows has.
const storeSDDL = "D:P(A;;FA;;;%s)"

// checkPrivate refuses a credential file that any account but its user can
// open.
//
// SYSTEM and Administrators are allowed, as entries and as the owner, because
// they are what root is to 0600: an administrator can take ownership of any
// file whatever its ACL says, and a file created from an elevated shell is
// owned by Administrators. Refusing them would refuse every file Windows
// creates in a profile and protect nothing. Any other owner is refused, because
// an owner can rewrite the ACL whatever it says today.
func checkPrivate(path string, _ fs.FileInfo) error {
	others, err := othersWithAccess(path)
	if err != nil {
		return errs.Auth("STORE_UNREADABLE", "cannot read the ACL of %s", path).Wrap(err)
	}
	if len(others) == 0 {
		return nil
	}
	user := "your account"
	if sid, err := currentUser(); err == nil {
		user = accountName(sid)
	}
	return errs.Auth("STORE_PERMISSIONS",
		"%s is readable by other users", path).
		WithDetail("its ACL lets in %s", strings.Join(others, "; ")).
		WithRemedy(`run: icacls "%s" /inheritance:r /grant:r "%s:F"`, path, user)
}

// restrictPrivate replaces the DACL of a file just created for the store with
// one granting the current user alone, before anything is written to it.
func restrictPrivate(f *os.File) error {
	user, err := currentUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(storeSDDL, user.String()))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

// othersWithAccess describes every way the file's security lets in an account
// outside trustedSIDs, and is empty when there is none.
func othersWithAccess(path string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	if sd == nil {
		// A filesystem that keeps no security, FAT among them: every account
		// that can reach the volume can read the file.
		return []string{"every account, because its filesystem keeps no ACL"}, nil
	}
	trusted, err := trustedSIDs()
	if err != nil {
		return nil, err
	}

	var others []string
	if owner, _, err := sd.Owner(); err != nil {
		return nil, err
	} else if !slices.ContainsFunc(trusted, owner.Equals) {
		others = append(others, "its owner, "+accountName(owner))
	}

	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && dacl == nil:
		// No DACL, or a null one, which Windows reads the same way: every
		// account allowed everything.
		return append(others, "every account, because it has no DACL"), nil
	case err != nil:
		return nil, err
	}
	entries, err := untrustedEntries(dacl, trusted)
	if err != nil {
		return nil, err
	}
	return append(others, entries...), nil
}

// untrustedEntries names each entry of a DACL that grants something to an
// account outside trusted.
//
// Any grant counts, not only read, for the reason the Unix check refuses any
// bit for group or other: write lets an account replace the credential, and
// WRITE_DAC lets it grant itself the rest. A denial only ever takes access
// away, and an inherit-only entry applies to what a directory creates rather
// than to the directory's files, so neither can let anybody in.
func untrustedEntries(dacl *windows.ACL, trusted []*windows.SID) ([]string, error) {
	var out []string
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			// The SID starts where SidStart sits and runs to the end of the
			// entry, which is how Windows lays one out.
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !slices.ContainsFunc(trusted, sid.Equals) {
				out = append(out, accountName(sid))
			}
		default:
			// An object or callback entry, which nothing writes on a file in a
			// profile. Unread is not the same as harmless, so it counts.
			out = append(out, fmt.Sprintf("an ACL entry of type %d that jr does not read",
				ace.Header.AceType))
		}
	}
	return out, nil
}

// trustedSIDs are the accounts the store may be open to: the user running this
// process, SYSTEM, and Administrators.
func trustedSIDs() ([]*windows.SID, error) {
	user, err := currentUser()
	if err != nil {
		return nil, err
	}
	out := []*windows.SID{user}
	for _, known := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid,
	} {
		sid, err := windows.CreateWellKnownSid(known)
		if err != nil {
			return nil, err
		}
		out = append(out, sid)
	}
	return out, nil
}

// currentUser is the account this process runs as.
func currentUser() (*windows.SID, error) {
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return token.User.Sid, nil
}

// accountName is DOMAIN\name and the SID, or the SID alone when the account
// cannot be looked up, which is how a deleted account shows in an ACL.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain != "" {
		account = domain + `\` + account
	}
	return account + " (" + sid.String() + ")"
}
