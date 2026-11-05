package main

import (
	"net"
	"runtime"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestTerminalDirectoryFollowRespectsManualNavigationAndBusy(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{term: term, shell: &remoteShell{}, fileDir: "/initial", fileFollowTerminal: true, workspace: "files"}
	term.Feed([]byte("\x1b]7;file://remote/one%20two\x07"))
	if !d.applyTerminalDirectory(term.Dir(), false) || d.fileDir != "/one two" {
		t.Fatalf("OSC 7 not followed: %q", d.fileDir)
	}
	d.fileDir = "/manual"
	if d.applyTerminalDirectory(term.Dir(), false) || d.fileDir != "/manual" {
		t.Fatal("unchanged terminal directory overrode manual navigation")
	}
	term.Feed([]byte("\x1b]7;file://remote/another\x07"))
	d.busy = true
	if d.applyTerminalDirectory(term.Dir(), false) {
		t.Fatal("changed directory during operation")
	}
	d.busy = false
	if !d.applyTerminalDirectory(term.Dir(), false) || d.fileDir != "/another" {
		t.Fatal("new directory was not followed after operation")
	}
	d.fileDir = "/manual"
	view := ui.NewTester(d.view, 1200, 1400)
	if err := view.Click("定位终端目录"); err != nil {
		t.Fatal(err)
	}
	if d.fileDir != "/another" {
		t.Fatal("manual locate did not use reported directory")
	}
}

func TestTerminalDirectoryValidationAndSessionIsolation(t *testing.T) {
	for _, value := range []string{"", "relative", "/bad\npath"} {
		if _, err := terminalDirectory(value, false); err == nil {
			t.Fatalf("accepted invalid remote directory %q", value)
		}
	}
	if value, err := terminalDirectory("/var/../home", false); err != nil || value != "/home" {
		t.Fatalf("unexpected remote path %q %v", value, err)
	}
	if runtime.GOOS == "windows" {
		if value, err := terminalDirectory("/C:/Users/test", true); err != nil || value != `C:\Users\test` {
			t.Fatalf("unexpected Windows path %q %v", value, err)
		}
		if _, err := terminalDirectory(`\\server\share`, true); err == nil {
			t.Fatal("accepted network path as local terminal directory")
		}
	}
	d := &desktop{currentSession: 1, fileFollowTerminal: true, lastTerminalDir: "/one", sessions: []*workspaceSession{{id: 1}, {id: 2, lastTerminalDir: "/two"}}}
	d.activateSession(2)
	if d.fileFollowTerminal || d.lastTerminalDir != "/two" {
		t.Fatal("follow state crossed sessions")
	}
	d.activateSession(1)
	if !d.fileFollowTerminal || d.lastTerminalDir != "/one" {
		t.Fatal("follow state did not return with session")
	}
}
