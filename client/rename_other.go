//go:build !windows && !linux

package main

import "errors"

func renameLocalNoReplace(source, target string) error {
	return errors.New("当前系统不支持安全的防覆盖重命名")
}
