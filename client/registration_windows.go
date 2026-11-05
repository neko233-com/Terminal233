//go:build windows

package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func localRegistrationPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "NekoSSH", "registration.secure")
}

func localRegistrationAvailable(url string) bool {
	if strings.TrimRight(strings.TrimSpace(url), "/") != defaultCloudURL {
		return false
	}
	path := localRegistrationPath()
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 16384
}

func readLocalRegistrationToken() (string, error) {
	data, err := os.ReadFile(localRegistrationPath())
	if err != nil || len(data) > 16384 {
		return "", errors.New("无法读取本机注册令牌")
	}
	return decryptRegistrationToken(string(data))
}

// The file contains only the registration token, protected by Windows DPAPI
// for the current user. It is never included in the repository or installer.
func decryptRegistrationToken(encoded string) (string, error) {
	data, err := hex.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(data) == 0 || len(data) > 8192 {
		return "", errors.New("本机注册令牌格式无效")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", errors.New("无法解锁本机注册令牌")
	}
	runtime.KeepAlive(data)
	if out.Data == nil || out.Size == 0 || out.Size > 4096 || out.Size%2 != 0 {
		if out.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
		}
		return "", errors.New("本机注册令牌格式无效")
	}
	plain := unsafe.Slice(out.Data, int(out.Size))
	words := make([]uint16, len(plain)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(plain[i*2:])
	}
	for i := range plain {
		plain[i] = 0
	}
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	token := string(utf16.Decode(words))
	for i := range words {
		words[i] = 0
	}
	if len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, "\x00\r\n") {
		return "", errors.New("本机注册令牌格式无效")
	}
	return token, nil
}
