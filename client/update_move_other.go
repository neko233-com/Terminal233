//go:build !windows

package main

import "os"

// The temporary file is created in the destination directory, so a hard link
// provides no-replace publication on filesystems that support it.
func moveUpdateNoReplace(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return os.Remove(source)
}
