package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	native "github.com/egoist/mygo/internal/windows"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestMain(m *testing.M) {
	result := make(chan int, 1)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() { go func() { result <- m.Run(); mygo.App.Quit() }() })
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(<-result)
}
func TestNativeOLEBatchDrop(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	files := []string{filepath.Join(source, "a.txt"), filepath.Join(source, "b.txt")}
	for _, name := range files {
		if err := os.WriteFile(name, []byte(filepath.Base(name)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{term: term, fileDir: dest, currentSession: 1, sessions: []*workspaceSession{{id: 1, term: term, fileDir: dest}}}
	w := mygo.NewWindow(mygo.WindowOptions{Title: "NekoSSH native drop test", Hidden: false, Width: 700, Height: 500, Content: ui.View(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() { d.fileDropView(c, true); ui.Text(c, d.fileStatus) })
	})})
	defer w.Destroy()
	mygo.RunOnMain(func() { d.win = w })
	drop := func(paths []string) {
		t.Helper()
		var over, dropped bool
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mygo.RunOnMain(func() { over, dropped = native.TestDropFiles(w.NativeHandle(), 25, 25, paths) })
			if over && dropped {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("native OLE drop rejected: over=%v dropped=%v", over, dropped)
	}
	awaitResult := func(expected string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		var status string
		for time.Now().Before(deadline) {
			var busy bool
			mygo.RunOnMain(func() { status, busy = d.fileStatus, d.busy })
			if !busy && strings.Contains(status, expected) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("native drop did not complete: %s", status)
	}
	drop(files)
	awaitResult("成功 2，失败 0")
	for _, file := range files {
		got, err := os.ReadFile(filepath.Join(dest, filepath.Base(file)))
		if err != nil || string(got) != filepath.Base(file) {
			t.Fatalf("copy mismatch %s: %q %v", file, got, err)
		}
	}
	third := filepath.Join(source, "c.txt")
	if err := os.WriteFile(third, []byte("third"), 0600); err != nil {
		t.Fatal(err)
	}
	drop([]string{files[0], third})
	awaitResult("成功 1，失败 1")
	got, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	if err != nil || string(got) != "a.txt" {
		t.Fatal("native drop overwrote an existing file")
	}
	got, err = os.ReadFile(filepath.Join(dest, "c.txt"))
	if err != nil || string(got) != "third" {
		t.Fatal("native drop did not continue after collision")
	}
}
