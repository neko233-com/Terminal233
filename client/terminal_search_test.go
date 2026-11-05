package main

import (
	"net"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestTerminalSearchUsesCurrentSessionAndCanClose(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("needle one\r\nneedle two\r\n"))
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, sessions: []*workspaceSession{{id: 1, label: "one", term: term}, {id: 2, label: "two"}}}
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("查找"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("查找终端输出"); err != nil {
		t.Fatal(err)
	}
	view.Type("needle")
	for i := 0; i < 5; i++ {
		view.Frame()
	}
	if got := term.SearchStatus(); got.Text != "needle" || got.Total != 2 {
		t.Fatalf("terminal search did not find output: %+v", got)
	}
	before := term.SearchStatus().Selected
	if err := view.Click("下一处"); err != nil {
		t.Fatal(err)
	}
	if term.SearchStatus().Selected == before {
		t.Fatal("next match did not change the selection")
	}
	d.activateSession(2)
	if d.terminalSearchOpen || d.terminalSearchText != "" {
		t.Fatal("search leaked into another session")
	}
	d.activateSession(1)
	if !d.terminalSearchOpen || d.terminalSearchText != "needle" {
		t.Fatal("search was not restored with its session")
	}
	d.closeTerminalSearch()
	if d.terminalSearchOpen || d.terminalSearchText != "" || term.SearchStatus().Text != "" {
		t.Fatal("closing search did not clear its state and highlights")
	}
	view.Frame()
	view.Key(ui.Cmd|ui.Shift, ui.KeyF)
	if !d.terminalSearchOpen {
		t.Fatal("search shortcut did not open the panel")
	}
}
