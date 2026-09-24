//go:build windows

package auth_test

import (
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/kmoneil/jr/internal/auth"
	"github.com/kmoneil/jr/internal/errs"
)

const storeSite = "acme.atlassian.invalid"

func currentUserSID(t *testing.T) *windows.SID {
	t.Helper()
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("reading this process's user: %v", err)
	}
	return token.User.Sid
}

// saved writes a credential through the store and returns the store and its
// file.
func saved(t *testing.T) (auth.FileStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.toml")
	store := auth.FileStore{Path: path}
	if err := store.Save(storeSite, auth.Credential{
		Scheme: auth.Bearer, Secret: auth.Secret(theToken),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	return store, path
}

// setDACL replaces a file's DACL with the one an SDDL string describes.
func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("parsing %q: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("the DACL of %q: %v", sddl, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("setting %q on %s: %v", sddl, path, err)
	}
}

// TestTheStoreIsPrivateToItsUserOnWindows reads back the DACL the store wrote,
// rather than asking the store whether it approves of it: the write and the
// check arrived in one change, and a test that used one to judge the other
// would pass with both wrong.
func TestTheStoreIsPrivateToItsUserOnWindows(t *testing.T) {
	_, path := saved(t)

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the ACL of %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("reading the descriptor's flags: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("the DACL inherits from the directory; it should be protected: %s", sd)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("reading the DACL: %v", err)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("the DACL has %d entries, want the user's alone: %s", dacl.AceCount, sd)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatalf("reading the entry: %v", err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(currentUserSID(t)) {
		t.Errorf("the one entry is not a grant to this user: %s", sd)
	}

	if _, ok, err := (auth.FileStore{Path: path}).Lookup(storeSite); err != nil || !ok {
		t.Errorf("the store refuses the file it wrote: ok = %v, err = %v", ok, err)
	}
}

// TestAStoreOthersCanOpenIsRefusedOnWindows is TestOverlyOpenStoreIsRefused in
// the terms Windows has, and names who else can open it.
func TestAStoreOthersCanOpenIsRefusedOnWindows(t *testing.T) {
	user := currentUserSID(t).String()
	cases := []struct {
		name, sddl, names string
	}{
		{"everyone may read", "D:P(A;;FA;;;" + user + ")(A;;FR;;;WD)", "S-1-1-0"},
		{"every user may read", "D:P(A;;FA;;;" + user + ")(A;;FR;;;BU)", "S-1-5-32-545"},
		{"a write is a grant too", "D:P(A;;FA;;;" + user + ")(A;;FW;;;AU)", "S-1-5-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := saved(t)
			setDACL(t, path, tc.sddl)

			_, _, err := store.Lookup(storeSite)
			e := errs.Coerce(err)
			if e == nil || e.Code != "STORE_PERMISSIONS" {
				t.Fatalf("lookup under %s: %v, want STORE_PERMISSIONS", tc.sddl, err)
			}
			if !strings.Contains(e.Detail, tc.names) {
				t.Errorf("the detail does not name %s: %s", tc.names, e.Detail)
			}
			if !strings.Contains(e.Remedy, "icacls") {
				t.Errorf("the remedy names no Windows command: %s", e.Remedy)
			}
		})
	}
}

// TestAStoreWithANullDACLIsRefusedOnWindows covers the one DACL that grants
// everything without naming anybody: none at all.
func TestAStoreWithANullDACLIsRefusedOnWindows(t *testing.T) {
	store, path := saved(t)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, nil, nil); err != nil {
		t.Fatalf("setting a null DACL: %v", err)
	}
	if _, _, err := store.Lookup(storeSite); errs.Coerce(err) == nil ||
		errs.Coerce(err).Code != "STORE_PERMISSIONS" {
		t.Fatalf("lookup under a null DACL: %v, want STORE_PERMISSIONS", err)
	}
}

// TestWhatWindowsHandsDownDoesNotMakeTheStorePublic holds the other side: what
// a profile directory gives every file in it, SYSTEM and Administrators beside
// the user, and a denial, which only ever takes access away.
func TestWhatWindowsHandsDownDoesNotMakeTheStorePublic(t *testing.T) {
	user := currentUserSID(t).String()
	for _, sddl := range []string{
		"D:P(A;;FA;;;" + user + ")(A;;FA;;;SY)(A;;FA;;;BA)",
		"D:P(D;;FA;;;WD)(A;;FA;;;" + user + ")",
	} {
		store, path := saved(t)
		setDACL(t, path, sddl)
		if _, ok, err := store.Lookup(storeSite); err != nil || !ok {
			t.Errorf("lookup under %s: ok = %v, err = %v", sddl, ok, err)
		}
	}
}
