package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestCompletionCandidatesMatchPrefixWithoutControlSequences(t *testing.T) {
	snippets := []quickCommand{{Name: "deploy", Command: "git status --short"}, {Name: "unsafe", Command: "git status\nexit"}}
	got := completionCandidates("git s", snippets)
	if len(got) < 2 || got[0].value != "git status --short" || got[1].value != "git status" {
		t.Fatal("snippet or built-in suggestion missing", got)
	}
	if len(completionCandidates("git\x1b", snippets)) != 0 || len(completionCandidates("", snippets)) != 0 {
		t.Fatal("invalid prefix accepted")
	}
	for _, v := range got {
		if strings.ContainsAny(v.value, "\r\n") {
			t.Fatal("multiline command suggested")
		}
	}
}

func TestShellPromptBoundaryKeepsOutputAndDefersEOF(t *testing.T) {
	raw := "prompt$ \x1b]633;B\x07git"
	shell := &remoteShell{out: strings.NewReader(raw)}
	b := make([]byte, 1024)
	n, err := shell.Read(b)
	if err != nil || string(b[:n]) != "prompt$ \x1b]633;B\x07" {
		t.Fatal("prompt boundary was not split", n, err)
	}
	serial, ready := shell.NekoPromptState()
	if serial != 1 || !ready {
		t.Fatal("prompt boundary not marked")
	}
	n, err = shell.Read(b)
	if err != nil || string(b[:n]) != "git" {
		t.Fatal("remaining output not preserved", n, err)
	}
	n, err = shell.Read(b)
	if n != 0 || err != io.EOF {
		t.Fatal("stream did not finish")
	}
	// Ensure an underlying reader returning data and EOF together is deferred.
	shell = &remoteShell{out: &dataAndEOF{data: []byte(raw)}}
	n, err = shell.Read(b)
	if err != nil || n == 0 {
		t.Fatal("premature EOF", err)
	}
	n, err = shell.Read(b)
	if err != io.EOF || string(b[:n]) != "git" {
		t.Fatal("delayed EOF lost output", n, err)
	}
}

type dataAndEOF struct{ data []byte }

func (r *dataAndEOF) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.EOF
}

func TestCompletionAtVerifiedPromptInWorkspace(t *testing.T) {
	settings, fp, _ := testSSHServer(t)
	settings.Fingerprint = fp
	shell, term, err := openSSHSession(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, ok := any(term).(completionTerminal); !ok {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("completion overlay unavailable")
		}
		t.Skip("requires MyGo overlay")
	}
	other := groupTestTerminals(t, 1)[0]
	s := &workspaceSession{id: 1, label: "remote", term: term, shell: shell}
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, shell: shell, sessions: []*workspaceSession{s, {id: 2, label: "local", term: other}}}
	d.bindTerminalBroadcast(s)
	if err = d.createTerminalGroup("project", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1400, 900)
	if err = view.Click("终端：remote"); err != nil {
		t.Fatal(err)
	}
	if _, err = shell.Write([]byte("$ \x1b]633;B\x07git")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view.Frame()
		if _, ok := view.Find("补全：git status"); ok {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if _, ok := view.Find("补全：git status"); !ok {
		t.Fatalf("completion not shown; text: %v, line: %q", view.Texts(), term.Text())
	}
	group := d.terminalGroupFor(s.id)
	group.broadcast = true
	view.Frame()
	if _, ok := view.Find("补全：git status"); ok || d.handleCompletionKey(s, 0, ui.KeyTab) {
		t.Fatal("completion remained active while broadcasting")
	}
	group.broadcast = false
	view.Frame()
	if _, ok := view.Find("补全：git status"); !ok {
		t.Fatal("completion did not return after broadcast")
	}
	view.Key(0, ui.KeyDown)
	if s.completion.selected != 1 {
		t.Fatal("down arrow did not select a suggestion")
	}
	view.Key(0, ui.KeyUp)
	if s.completion.selected != 0 {
		t.Fatal("up arrow did not select a suggestion")
	}
	view.Key(0, ui.KeyTab)
	hook := any(term).(completionTerminal)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view.Frame()
		if line, _, _, _, _, ok := hook.NekoPromptLine(); ok && line == "git status" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if line, _, _, _, _, ok := hook.NekoPromptLine(); !ok || line != "git status" {
		t.Fatalf("Tab did not append the selected suffix: %q %v", line, ok)
	}
	// Broadcast and non-prompt phases never intercept a shell's Tab key.
	group.broadcast = true
	if d.handleCompletionKey(s, 0, ui.KeyTab) {
		t.Fatal("completion intercepted broadcast input")
	}
	group.broadcast = false
	if _, err = shell.Write([]byte("\x1b]633;C\x07")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if phase, _ := shell.integration.snapshot(); phase == shellRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, _, _, _, ok := hook.NekoPromptLine(); ok {
		t.Fatal("completion remained active after command execution")
	}
	// Emit a fresh prompt to exercise editing and alternate-screen guards.
	if _, err = shell.Write([]byte("\r\n$ \x1b]633;B\x07git status")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if line, _, _, _, _, ok := hook.NekoPromptLine(); ok && line == "git status" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	term.Feed([]byte("\x1b[D"))
	if _, _, _, _, _, ok := hook.NekoPromptLine(); ok {
		t.Fatal("completion accepted a middle-of-line cursor")
	}
	term.Feed([]byte("\x1b[?1049h"))
	if _, _, _, _, _, ok := hook.NekoPromptLine(); ok {
		t.Fatal("completion active in alternate screen")
	}
}

func TestCompletionSettingPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := writePreference(path, "autocomplete-disabled", true); err != nil {
		t.Fatal(err)
	}
	d := &desktop{}
	if err := d.loadPreferences(path); err != nil || !d.autocompleteDisabled {
		t.Fatal("autocomplete setting was lost", err)
	}
}

func TestCompletionSettingWorksInSettingsUI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	d := &desktop{}
	if err := d.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端自动补全"); err != nil {
		t.Fatal(err)
	}
	if !d.autocompleteDisabled {
		t.Fatal("setting did not disable completion")
	}
	persisted, err := readPreferences(path)
	if err != nil || !persisted.AutocompleteDisabled {
		t.Fatal("setting was not saved", err)
	}
}

func TestCompletionHookPreservesExistingTerminalKeyHandler(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	count := 0
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1, OnKey: func(_ ui.Modifiers, key ui.Key) bool {
		if key == ui.KeyF6 {
			count++
			return true
		}
		return false
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	hook, ok := any(term).(completionTerminal)
	if !ok {
		t.Skip("requires MyGo overlay")
	}
	hook.NekoSetCompletionKey(func(ui.Modifiers, ui.Key) bool { return false })
	hook.NekoSetCompletionKey(func(ui.Modifiers, ui.Key) bool { return false })
	view := ui.NewTester(func(c *ui.Context) { terminal.View(c, term).Label("终端测试").Fill() }, 800, 600)
	if err = view.Click("终端测试"); err != nil {
		t.Fatal(err)
	}
	view.Key(0, ui.KeyF6)
	if count != 1 {
		t.Fatalf("existing key handler called %d times", count)
	}
}

// A remote shell may echo slowly. Completion must never append a suffix to
// an older line while later keystrokes are still in flight.
type completionTestConn struct{ *remoteShell }

func (c completionTestConn) Resize(int, int) error { return nil }

func TestCompletionWaitsForAllRemoteEchoBeforeInterceptingTab(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	shell := &remoteShell{in: left, out: left}
	term, err := terminal.New(terminal.Options{Conn: completionTestConn{shell}, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, ok := any(term).(completionTerminal); !ok {
		t.Skip("requires MyGo overlay")
	}
	s := &workspaceSession{id: 1, label: "remote", term: term, shell: shell}
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, shell: shell, sessions: []*workspaceSession{s}}
	d.bindTerminalBroadcast(s)
	if err = d.createTerminalGroup("project", []int{1}, 1, false); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1000, 700)
	if err = view.Click("终端：remote"); err != nil {
		t.Fatal(err)
	}
	input := make(chan string, 10)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, e := right.Read(buf)
			if n > 0 {
				input <- string(buf[:n])
			}
			if e != nil {
				return
			}
		}
	}()
	sendOutput := func(text string) {
		t.Helper()
		done := make(chan error, 1)
		go func() { _, e := right.Write([]byte(text)); done <- e }()
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("output stalled")
		}
	}
	waitSuggestion := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			view.Frame()
			_, ok := view.Find("补全：git status")
			if ok == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("suggestion visibility did not become %v", want)
	}
	sendOutput("$ \x1b]633;B\x07gi")
	waitSuggestion(true)
	view.TypeKey(0, ui.KeyT, "t")
	view.Frame()
	if _, ok := view.Find("补全：git status"); ok {
		t.Fatal("suggestion remained active while text was in flight")
	}
	view.Key(0, ui.KeyTab)
	received := ""
	deadline := time.After(5 * time.Second)
	for !strings.Contains(received, "\t") {
		select {
		case part := <-input:
			received += part
		case <-deadline:
			t.Fatal("shell did not receive original input")
		}
	}
	if received != "t\t" {
		t.Fatalf("stale completion changed input: %q", received)
	}
	sendOutput("t")
	view.Frame()
	if _, ok := view.Find("补全：git status"); ok {
		t.Fatal("unacknowledged shell Tab unexpectedly reopened completion")
	}
	// A new prompt resets the conservative guard; partial echoes must still wait.
	sendOutput("\r\n$ \x1b]633;B\x07gi")
	waitSuggestion(true)
	view.TypeKey(0, ui.KeyT, "t")
	view.TypeKey(0, ui.KeySpace, " ")
	sendOutput("t")
	view.Frame()
	if _, ok := view.Find("补全：git status"); ok {
		t.Fatal("partial echo re-enabled stale completion")
	}
	sendOutput(" ")
	waitSuggestion(true)
}

func TestCompletionDoesNotRetainUnacknowledgedPlaintext(t *testing.T) {
	s := &workspaceSession{}
	s.completion.pending = true
	s.completion.expectedHashes = completionHashes("git ")
	secret := "synthetic-unacknowledged-secret"
	d := &desktop{}
	d.noteCompletionText(s, secret, false)
	if !s.completion.pending || s.completion.uncertain {
		t.Fatal("echo guard did not remain active")
	}
	if strings.Contains(fmt.Sprintf("%+v", s.completion), secret) {
		t.Fatal("unacknowledged plaintext was retained")
	}
	hashes := completionHashes("git " + secret)
	if len(hashes) != len(s.completion.expectedHashes) || hashes[len(hashes)-1] != s.completion.expectedHashes[len(s.completion.expectedHashes)-1] {
		t.Fatal("echo guard does not match the echoed text")
	}
	s.completion.invalidate()
	if s.completion.pending || len(s.completion.expectedHashes) != 0 {
		t.Fatal("invalidated echo guard retained state")
	}
}
