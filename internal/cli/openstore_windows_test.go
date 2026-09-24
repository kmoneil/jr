//go:build windows

package cli_test

import (
	"testing"

	"golang.org/x/sys/windows"
)

// openToOthers makes a credential store readable by other users, which the
// store refuses to read. A mode does that on Unix and does nothing here, where
// what opens a file is its ACL: this one grants Everyone read beside the user.
func openToOthers(t *testing.T, path string) {
	t.Helper()
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("reading this process's user: %v", err)
	}
	sd, err := windows.SecurityDescriptorFromString(
		"D:P(A;;FA;;;" + token.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatalf("building the DACL: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("reading the DACL back: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("opening %s to Everyone: %v", path, err)
	}
}

// storeRemedy is the command a STORE_PERMISSIONS remedy names here.
const storeRemedy = "icacls"
