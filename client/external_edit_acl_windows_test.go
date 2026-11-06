//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestExternalEditCopyUsesPrivateWindowsACL(t *testing.T) {
	filename, err := createEditCopy("fixture.txt", []byte("private"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filename); _ = os.Remove(filepath.Dir(filename)) }()
	sd, err := windows.GetNamedSecurityInfo(filepath.Dir(filename), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	text := sd.String()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, user.User.Sid.String()) || strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;BU)") {
		t.Fatalf("unexpected edit directory ACL: %s", text)
	}
	if got, _, err := readLocalEdit(filename); err != nil || string(got) != "private" {
		t.Fatalf("cannot read edit copy: %v", err)
	}
}
