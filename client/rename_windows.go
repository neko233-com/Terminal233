//go:build windows

package main

// MoveFile provides atomic no-replace renames for files and directories.
func renameLocalNoReplace(source, target string) error {
	return moveUpdateNoReplace(source, target)
}
