//go:build !windows

package main

import "errors"

func protectProfileCredentials(s connectionSettings) (string, error) {
	if s.Password == "" && s.Passphrase == "" {
		return "", nil
	}
	return "", errors.New("当前平台无法安全保存本机凭据")
}

func unprotectProfileCredentials(_ string, _ connectionSettings) (string, string, error) {
	return "", "", errors.New("当前平台无法解锁本机凭据")
}
