//go:build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Bind the protected credentials to their endpoint, so replacing metadata in
// the profile file cannot cause a password to be used for a different host.
type protectedProfileCredentials struct {
	Host, Port, User, KeyPath, Password, Passphrase string
	JumpID                                          string `json:",omitempty"`
}

func protectProfileCredentials(s connectionSettings) (string, error) {
	if s.Password == "" && s.Passphrase == "" {
		return "", nil
	}
	if len(s.Password)+len(s.Passphrase) > 16384 {
		return "", errors.New("凭据过长，无法保存")
	}
	plain, err := json.Marshal(protectedProfileCredentials{Host: s.Host, Port: s.Port, User: s.User, KeyPath: s.KeyPath, Password: s.Password, Passphrase: s.Passphrase, JumpID: s.JumpID})
	if err != nil {
		return "", errors.New("无法保护本机凭据")
	}
	defer clear(plain)
	if len(plain) > 32768 {
		return "", errors.New("凭据过长，无法保存")
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(plain)
	if err != nil || out.Data == nil {
		return "", errors.New("无法保护本机凭据")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size == 0 || out.Size > 49152 {
		return "", errors.New("本机凭据过长")
	}
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, int(out.Size))), nil
}

func unprotectProfileCredentials(encoded string, s connectionSettings) (string, string, error) {
	if len(encoded) > 65536 {
		return "", "", errors.New("无法解锁本机凭据")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 49152 {
		return "", "", errors.New("无法解锁本机凭据")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(data)
	if err != nil || out.Data == nil {
		return "", "", errors.New("无法解锁本机凭据")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size == 0 || out.Size > 32768 {
		return "", "", errors.New("无法解锁本机凭据")
	}
	plain := unsafe.Slice(out.Data, int(out.Size))
	defer clear(plain)
	var credentials protectedProfileCredentials
	if err = json.Unmarshal(plain, &credentials); err != nil || credentials.Host != s.Host || credentials.Port != s.Port || credentials.User != s.User || credentials.KeyPath != s.KeyPath || credentials.JumpID != s.JumpID {
		return "", "", errors.New("无法解锁本机凭据")
	}
	return credentials.Password, credentials.Passphrase, nil
}
