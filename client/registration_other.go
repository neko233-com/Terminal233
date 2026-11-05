//go:build !windows

package main

import "errors"

func localRegistrationAvailable(string) bool { return false }
func readLocalRegistrationToken() (string, error) {
	return "", errors.New("当前系统不支持读取本机注册令牌")
}
