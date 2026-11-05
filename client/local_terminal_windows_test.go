//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
)

func TestPowerShellReportsDirectoryAndPreservesPrompt(t *testing.T) {
	executable, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "目录 with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	customPrompt := `function global:prompt { "NKS_PROMPT:$global:LASTEXITCODE/$?> " }; `
	term, err := terminal.New(terminal.Options{Command: []string{executable, "-NoProfile", "-NoLogo", "-NoExit", "-Command", customPrompt + powerShellIntegration}, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	deadline := time.Now().Add(20 * time.Second)
	for term.Dir() == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if term.Dir() == "" {
		t.Fatal("PowerShell did not report its initial directory")
	}
	quoted := strings.ReplaceAll(dir, "'", "''")
	command := fmt.Sprintf("Set-Location -LiteralPath '%s'; $global:LASTEXITCODE=37; Write-Output 'NKS_COMMAND_OK'; Write-Error 'expected' -ErrorAction Ignore\r", quoted)
	term.Send([]byte(command))
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		actual, pathErr := terminalDirectory(term.Dir(), true)
		snapshot := term.Snapshot()
		if pathErr == nil && strings.EqualFold(actual, dir) && bytes.Contains(snapshot, []byte("NKS_PROMPT:37/False>")) && bytes.Contains(snapshot, []byte("NKS_COMMAND_OK")) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("directory or prompt integration failed: directory=%q snapshot=%q", term.Dir(), term.Snapshot())
}
