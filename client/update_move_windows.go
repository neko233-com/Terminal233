//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// MoveFile does not replace an existing destination on Windows.
func moveUpdateNoReplace(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err = windows.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "move", Old: source, New: target, Err: err}
	}
	return nil
}
