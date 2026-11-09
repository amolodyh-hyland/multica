package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func setControlTokenDACL(t *testing.T, path, descriptor string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func makeControlTokenPrivate(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setControlTokenDACL(t, path, "D:P(A;;FA;;;"+user.User.Sid.String()+")")
}

func TestLocalControlWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	makeControlTokenPrivate(t, path)
	if _, err := loadLocalControlToken(path); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range []string{
		"D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;GR;;;WD)",
		"D:NO_ACCESS_CONTROL",
	} {
		setControlTokenDACL(t, path, descriptor)
		if _, err := loadLocalControlToken(path); err == nil {
			t.Fatal("accepted public/null token DACL")
		}
	}
	makeControlTokenPrivate(t, path)
}
