package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestShellIntegrationStreamsAndRejectsUntrustedText(t *testing.T) {
	var s shellIntegration
	feedBytes := func(value string) {
		for _, b := range []byte(value) {
			s.feed([]byte{b})
		}
	}
	feedBytes("ordinary password input does not create state\x1b]133;A\x07")
	if phase, _ := s.snapshot(); phase != shellPromptStart {
		t.Fatal("prompt start was not distinguished")
	}
	feedBytes("prompt\x1b]133;B\x1b\\")
	if phase, command := s.snapshot(); phase != shellPrompt || command != "" {
		t.Fatal("prompt state incorrect")
	}
	feedBytes("\x1b]633;E;tail\\x20-f\\x20/tmp/a\\x3bb;nonce\x07\x1b]633;C\x1b\\")
	if phase, command := s.snapshot(); phase != shellRunning || command != "tail -f /tmp/a;b" {
		t.Fatalf("running command incorrect: %v %q", phase, command)
	}
	feedBytes("password typed into an application is not observed\x1b]633;D;0\x07")
	if phase, command := s.snapshot(); phase != shellFinished || command != "" {
		t.Fatal("finished command retained")
	}
	// Marker-like bytes inside a DCS control string are never shell markers.
	feedBytes("\x1bPignored\x1b]633;E;forged\x07\x1b]633;C\x07\x1b\\")
	if phase, _ := s.snapshot(); phase != shellFinished {
		t.Fatal("control string spoofed shell state")
	}
	feedBytes("\x1b]633;E;" + strings.Repeat("x", maxShellSequence+100) + "\x07\x1b]633;C\x07")
	if phase, command := s.snapshot(); phase != shellRunning || command != "" || len(s.seq) > maxShellSequence {
		t.Fatal("oversized marker was accepted")
	}
	feedBytes("\x1b]633;D;1\x07\x1b]633;E;bad\\x0aline\x07\x1b]633;C\x07")
	if _, command := s.snapshot(); command != "" {
		t.Fatal("unsafe command text accepted")
	}
	feedBytes("\x1b]52;c;" + strings.Repeat("private", 100) + "\x07")
	if len(s.seq) != 0 || s.overflow {
		t.Fatal("unrelated OSC payload remained buffered")
	}
	if cap(s.seq) > 0 {
		for _, b := range s.seq[:cap(s.seq)] {
			if b != 0 {
				t.Fatal("unrelated OSC bytes remained in buffer")
			}
		}
	}
	feedBytes("\x1b]633;D\x07\x1b]633;E;echo\\x20中文\x07\x1b]633;C\x07")
	if _, command := s.snapshot(); command != "echo 中文" {
		t.Fatal("UTF-8 command was not preserved")
	}
}

func TestShellIntegrationReadPreservesBytes(t *testing.T) {
	raw := "before\x1b]633;E;echo\\x20ok\x07\x1b]633;C\x07after"
	shell := &remoteShell{out: strings.NewReader(raw)}
	var output strings.Builder
	buf := make([]byte, 3)
	for {
		n, err := shell.Read(buf)
		output.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != raw {
		t.Fatal("terminal output was altered")
	}
	if phase, command := shell.integration.snapshot(); phase != shellRunning || command != "echo ok" {
		t.Fatal("fragmented stream not recognized")
	}
}

func TestShellIntegrationTracksSSHSessionAndWorkspaceUI(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, term, err := openSSHSession(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	marker := "\x1b]633;E;tail\\x20-f\\x20app.log\x07\x1b]633;C\x07"
	if _, err = shell.Write([]byte(marker)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if phase, command := shell.integration.snapshot(); phase == shellRunning && command == "tail -f app.log" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if phase, command := shell.integration.snapshot(); phase != shellRunning || command != "tail -f app.log" {
		t.Fatal("SSH output state not detected")
	}
	other := groupTestTerminals(t, 1)[0]
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, shell: shell, sessions: []*workspaceSession{{id: 1, label: "remote", term: term, shell: shell}, {id: 2, label: "local", term: other}}}
	if err = d.createTerminalGroup("project", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1400, 900)
	if _, ok := view.Find("运行中的命令：tail -f app.log"); !ok {
		t.Fatal("running command missing in focus view")
	}
	if err = view.Click("分屏查看"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Find("运行中的命令：tail -f app.log"); !ok {
		t.Fatal("running command missing in split view")
	}
	if _, err = shell.Write([]byte("\x1b]633;D;0\x07")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if phase, _ := shell.integration.snapshot(); phase == shellFinished {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	view.Frame()
	if _, ok := view.Find("运行中的命令：tail -f app.log"); ok {
		t.Fatal("finished command still visible")
	}
}
