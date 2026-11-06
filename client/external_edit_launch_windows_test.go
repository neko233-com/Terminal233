//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExternalEditorLaunchPassesExactFilePath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "helper.go")
	program := `package main
import ("os")
func main() { if len(os.Args)==2 { _ = os.WriteFile(os.Args[1], []byte("opened"), 0600) } }
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(dir, "test editor.exe")
	build := exec.Command("go", "build", "-p", "1", "-ldflags=-H=windowsgui", "-o", editor, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build editor helper: %v %s", err, output)
	}
	target := filepath.Join(dir, "file with spaces.txt")
	if err := startExternalEditor(editor, target); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if got, err := os.ReadFile(target); err == nil && string(got) == "opened" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("editor did not receive the file path")
}
