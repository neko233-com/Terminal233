//go:build e2e

package main

import (
	_ "github.com/egoist/mygo/plugins/automation"
	"os"
)

func init() {
	if os.Getenv("NEKOSSH_E2E_NO_EDITOR") == "1" {
		startExternalEditor = func(_, _ string) error { return nil }
	}
}
