//go:build windows

package main

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"

	"github.com/egoist/mygo/ui"
	"golang.org/x/sys/windows"
)

func TestLocalRegistrationTokenIsUserProtectedAndBoundToDefaultHost(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	token := strings.Repeat("a", 48)
	words := utf16.Encode([]rune(token))
	plain := make([]byte, len(words)*2)
	for i, word := range words {
		binary.LittleEndian.PutUint16(plain[i*2:], word)
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		t.Fatal(err)
	}
	encoded := hex.EncodeToString(unsafe.Slice(out.Data, int(out.Size)))
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	path := localRegistrationPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(encoded), 0600); err != nil {
		t.Fatal(err)
	}
	if !localRegistrationAvailable(defaultCloudURL) || localRegistrationAvailable("https://other.example") {
		t.Fatal("token exposure was not limited to the default endpoint")
	}
	got, err := readLocalRegistrationToken()
	if err != nil || got != token {
		t.Fatal("current user could not unlock registration token")
	}
	d := &desktop{workspace: "cloud", cloudURL: defaultCloudURL}
	view := ui.NewTester(d.view, 1200, 2200)
	if err := view.Click("使用本机部署注册令牌"); err != nil {
		t.Fatal(err)
	}
	if d.cloudRegistration != token {
		t.Fatal("registration form did not load the encrypted token")
	}
	d.cloudURL = "https://other.example"
	d.cloudURLChanged()
	view.Frame()
	if view.HasText("使用本机部署注册令牌") || d.cloudRegistration != "" || d.cloudRegistrationLocal {
		t.Fatal("local token remained available for a different service")
	}
	if _, err := decryptRegistrationToken("not-hex"); err == nil {
		t.Fatal("invalid token file accepted")
	}
}
