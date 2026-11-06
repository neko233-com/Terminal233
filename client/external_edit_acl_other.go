//go:build !windows

package main

import "os"

func protectEditDirectory(dir string) error { return os.Chmod(dir, 0700) }
