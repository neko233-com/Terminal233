package main

import (
	"bytes"
	"net"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
)

func TestTerminalPreservesANSIColor(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("\x1b[38;2;12;34;56mhighlight\x1b[0m"))
	snapshot := term.Snapshot()
	if !bytes.Contains(snapshot, []byte("highlight")) || !bytes.Contains(snapshot, []byte("12;34;56")) {
		t.Fatalf("ANSI color not preserved: %q", snapshot)
	}
}
