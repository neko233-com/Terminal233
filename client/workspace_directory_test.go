package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestWorkspaceSavesReportedRemoteDirectoryAndVersion(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("\x1b]7;file://host/home/alice/project%20one\x07"))
	settings := connectionSettings{Host: "host", Port: "22", User: "alice", KeyPath: "key", Fingerprint: "SHA256:trusted"}
	savedProfile := profile{ID: "host", Label: "host", Settings: settings}
	session := &workspaceSession{id: 1, label: "host", term: term, shell: &remoteShell{}, active: settings, sourceProfileID: "host", fileDir: "/unrelated"}
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, shell: session.shell, active: settings, fileDir: "/unrelated", profiles: []profile{savedProfile}, sessions: []*workspaceSession{session}}
	w, err := d.workspaceSnapshot("work")
	if err != nil || w.Entries[0].TerminalDir != "/home/alice/project one" || w.Entries[0].FileDir != "/unrelated" {
		t.Fatalf("terminal and file directories not preserved independently: %+v %v", w, err)
	}
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	stored, err := updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil })
	if err != nil || stored.Version != 3 {
		t.Fatalf("remote directory schema not upgraded: %+v %v", stored, err)
	}
	read, err := readWorkspaceTemplates(filename)
	if err != nil || read.Items[0].Entries[0].TerminalDir != w.Entries[0].TerminalDir {
		t.Fatalf("directory did not round trip: %+v %v", read, err)
	}
	read.Version = 2
	data, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readWorkspaceTemplates(filename); err == nil {
		t.Fatal("old schema accepted remote directory")
	}
	for _, invalid := range []string{"relative", "/a/../b", "/bad\npath"} {
		bad := w
		bad.Entries = append([]workspaceTemplateEntry(nil), w.Entries...)
		bad.Entries[0].TerminalDir = invalid
		if validateSavedWorkspace(bad) == nil {
			t.Fatalf("accepted remote directory %q", invalid)
		}
	}
}

func TestWorkspaceShowsReportedDirectoryInFocusAndGrid(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	terms[0].Feed([]byte("\x1b]7;file://host/home/alpha\x07"))
	terms[1].Feed([]byte("\x1b]7;file://host/var/project\x07"))
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0], shell: &remoteShell{}}, {id: 2, label: "two", term: terms[1], shell: &remoteShell{}}}}
	if err := d.createTerminalGroup("project", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1200, 900)
	for _, dir := range []string{"/home/alpha", "/var/project"} {
		if _, ok := view.Find(dir); !ok {
			t.Fatalf("focus view omitted %s", dir)
		}
	}
	if err := view.Click("分屏查看"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/home/alpha", "/var/project"} {
		if _, ok := view.Find(dir); !ok {
			t.Fatalf("grid view omitted %s", dir)
		}
	}
}

func TestWorkspaceDirectoryUsesVerifiedSSHExec(t *testing.T) {
	commands := make(chan string, 1)
	settings, fingerprint, attempts := testSSHServerWithExec(t, nil, nil, nil, func(command string) { commands <- command })
	settings.Fingerprint = "SHA256:wrong"
	if shell, _, err := openSSHSessionAt(settings, terminal.Options{}, "/home/test"); err == nil {
		shell.Close()
		t.Fatal("accepted changed host key")
	}
	if attempts.Load() != 0 {
		t.Fatal("sent credentials before verifying the host key")
	}
	settings.Fingerprint = fingerprint
	shell, term, err := openSSHSessionAt(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1}, "/home/test")
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	defer shell.Close()
	want, _ := remoteDirectoryCommand("/home/test")
	select {
	case got := <-commands:
		if got != want {
			t.Fatalf("unexpected remote command: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote directory was not sent")
	}
}

func TestRemoteDirectoryCommandTreatsPathAsLiteral(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if runtime.GOOS == "windows" {
		sh = `C:\Program Files\Git\bin\sh.exe`
		_, err = os.Stat(sh)
	}
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "name '$(touch INJECTED)' space")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	posixDir := filepath.ToSlash(dir)
	if runtime.GOOS == "windows" {
		posixDir = "/" + strings.ToLower(posixDir[:1]) + posixDir[2:]
	}
	command, err := remoteDirectoryCommand(posixDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", command)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	cmd.Stdin = strings.NewReader("pwd\nexit\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), posixDir) {
		t.Fatalf("literal directory not restored: %q %v", out, err)
	}
	if _, err = os.Stat(filepath.Join(root, "INJECTED")); !os.IsNotExist(err) {
		t.Fatal("directory name was executed as shell code")
	}
	missing, _ := remoteDirectoryCommand(posixDir + "/missing")
	cmd = exec.Command(sh, "-c", missing)
	cmd.Dir, cmd.Env, cmd.Stdin = root, append(os.Environ(), "SHELL=/bin/sh"), strings.NewReader("exit\n")
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "无法恢复终端目录") {
		t.Fatalf("missing directory was not reported: %q %v", out, err)
	}
}
