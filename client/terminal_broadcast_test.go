package main

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestWorkspaceBroadcastKeyboardIMEPasteAndProtocolIsolation(t *testing.T) {
	sessions, peers := multiTestSessions(t, 3)
	if !supportsTerminalBroadcast(sessions[0].term) {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("required MyGo input hooks are missing")
		}
		t.Skip("build with Taskfile to enable the verified MyGo input hooks")
	}
	for i, s := range sessions {
		s.label = []string{"one", "two", "outside"}[i]
	}
	d := &desktop{workspace: "terminal", currentSession: 1, term: sessions[0].term, sessions: sessions}
	for _, session := range sessions {
		d.bindTerminalBroadcast(session)
	}
	if err := d.createTerminalGroup("project", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	group := d.terminalGroupFor(1)
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("广播输入到工作区"); err != nil {
		t.Fatal(err)
	}
	if !group.broadcast {
		t.Fatal("broadcast did not enable")
	}
	if err := view.Click("终端：one"); err != nil {
		t.Fatal(err)
	}
	read := func(i int, want string) {
		t.Helper()
		_ = peers[i].SetReadDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, len(want))
		if _, err := io.ReadFull(peers[i], b); err != nil || string(b) != want {
			t.Fatalf("terminal %d received %q, want %q: %v", i+1, b, want, err)
		}
	}
	quiet := func(i int) {
		t.Helper()
		_ = peers[i].SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := peers[i].Read(make([]byte, 1)); err == nil {
			t.Fatalf("unexpected input on terminal %d", i+1)
		}
	}
	view.TypeKey(0, ui.KeyA, "a")
	read(0, "a")
	read(1, "a")
	view.Key(ui.Ctrl, ui.KeyC)
	read(0, "\x03")
	read(1, "\x03")
	view.Compose("中文", 2)
	view.Type("中文")
	read(0, "中文")
	read(1, "中文")
	quiet(2)
	sessions[1].term.Feed([]byte("\x1b[?1h"))
	view.Key(0, ui.KeyUp)
	read(0, "\x1b[A")
	read(1, "\x1bOA")
	sessions[1].term.Feed([]byte("\x1b[?2004h"))
	view.SetClipboard("粘贴")
	view.Command("paste")
	read(0, "粘贴")
	read(1, "\x1b[200~粘贴\x1b[201~")
	sessions[0].term.Feed([]byte("\x1b[5n"))
	read(0, "\x1b[0n")
	quiet(1)
	if err := view.Click("分屏查看"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端：two"); err != nil {
		t.Fatal(err)
	}
	view.Type("reverse")
	read(1, "reverse")
	read(0, "reverse")
	if err := view.Click("关闭广播输入"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端：one"); err != nil {
		t.Fatal(err)
	}
	view.Type("x")
	read(0, "x")
	quiet(1)
}

func TestWorkspaceBroadcastStopsBeforeSendingToOtherLiveTargetsWhenOneEnds(t *testing.T) {
	sessions, peers := multiTestSessions(t, 3)
	if !supportsTerminalBroadcast(sessions[0].term) {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("required MyGo input hooks are missing")
		}
		t.Skip("build with Taskfile to enable the verified MyGo input hooks")
	}
	for i, s := range sessions {
		s.label = []string{"one", "two", "three"}[i]
	}
	d := &desktop{workspace: "terminal", currentSession: 1, term: sessions[0].term, sessions: sessions}
	for _, session := range sessions {
		d.bindTerminalBroadcast(session)
	}
	if err := d.createTerminalGroup("project", []int{1, 2, 3}, 1, false); err != nil {
		t.Fatal(err)
	}
	group := d.terminalGroupFor(1)
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("广播输入到工作区"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端：one"); err != nil {
		t.Fatal(err)
	}
	_ = sessions[2].term.Close()
	view.Type("x")
	_ = peers[0].SetReadDeadline(time.Now().Add(3 * time.Second))
	if b := make([]byte, 1); func() bool { _, err := io.ReadFull(peers[0], b); return err != nil || string(b) != "x" }() {
		t.Fatal("source did not receive typed input")
	}
	_ = peers[1].SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := peers[1].Read(make([]byte, 1)); err == nil {
		t.Fatal("partial broadcast reached a remaining target")
	}
	if group.broadcast {
		t.Fatal("broadcast stayed enabled after a target exited")
	}
}

func TestWorkspaceBroadcastStopsWhenMembersChange(t *testing.T) {
	sessions, _ := multiTestSessions(t, 3)
	if !supportsTerminalBroadcast(sessions[0].term) {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("required MyGo input hooks are missing")
		}
		t.Skip("build with Taskfile to enable the verified MyGo input hooks")
	}
	d := &desktop{workspace: "terminal", currentSession: 1, term: sessions[0].term, sessions: sessions}
	if err := d.createTerminalGroup("project", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	group := d.terminalGroupFor(1)
	d.toggleGroupBroadcast(group)
	if !group.broadcast {
		t.Fatal("broadcast did not enable")
	}
	if err := d.addTerminalGroupMember(group, 3); err != nil {
		t.Fatal(err)
	}
	if group.broadcast {
		t.Fatal("adding a member retained broadcast")
	}
	d.toggleGroupBroadcast(group)
	d.detachGroupSession(3)
	if group.broadcast {
		t.Fatal("removing a member retained broadcast")
	}
}

func TestWorkspaceBroadcastSelectsParticipantsInFocusAndGrid(t *testing.T) {
	sessions, peers := multiTestSessions(t, 4)
	if !supportsTerminalBroadcast(sessions[0].term) {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("required MyGo input hooks are missing")
		}
		t.Skip("build with Taskfile to enable the verified MyGo input hooks")
	}
	for i, s := range sessions {
		s.label = []string{"one", "two", "three", "outside"}[i]
	}
	d := &desktop{workspace: "terminal", currentSession: 1, term: sessions[0].term, sessions: sessions}
	for _, session := range sessions {
		d.bindTerminalBroadcast(session)
	}
	if err := d.createTerminalGroup("project", []int{1, 2, 3}, 1, false); err != nil {
		t.Fatal(err)
	}
	group := d.terminalGroupFor(1)
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("广播输入到工作区"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("移出广播：three · 会话 3"); err != nil {
		t.Fatal(err)
	}
	if len(group.broadcastMembers) != 2 || group.broadcastMembers[3] {
		t.Fatal("third participant not excluded")
	}
	read := func(i int, want string) {
		t.Helper()
		_ = peers[i].SetReadDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, len(want))
		if _, err := io.ReadFull(peers[i], b); err != nil || string(b) != want {
			t.Fatalf("terminal %d received %q, want %q: %v", i+1, b, want, err)
		}
	}
	quiet := func(i int) {
		t.Helper()
		_ = peers[i].SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := peers[i].Read(make([]byte, 1)); err == nil {
			t.Fatalf("unexpected input on terminal %d", i+1)
		}
	}
	if err := view.Click("终端：one"); err != nil {
		t.Fatal(err)
	}
	view.Type("A")
	read(0, "A")
	read(1, "A")
	quiet(2)
	quiet(3)
	if err := view.Click("分屏查看"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("移出广播：two · 会话 2"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端：two"); err != nil {
		t.Fatal(err)
	}
	view.Type("B")
	read(1, "B")
	quiet(0)
	quiet(2)
	if !group.broadcast || len(group.broadcastMembers) != 1 {
		t.Fatal("single participant should remain editable")
	}
	if err := view.Click("加入广播：three · 会话 3"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("终端：three"); err != nil {
		t.Fatal(err)
	}
	view.Type("C")
	read(2, "C")
	read(0, "C")
	quiet(1)
	_ = sessions[1].term.Close() // excluded sessions cannot interrupt active participants
	view.Type("D")
	read(2, "D")
	read(0, "D")
	if !group.broadcast {
		t.Fatal("excluded disconnected session stopped the broadcast")
	}
	if err := view.Click("关闭广播输入"); err != nil {
		t.Fatal(err)
	}
	if group.broadcast || group.broadcastMembers != nil {
		t.Fatal("broadcast selection was retained after disabling")
	}
}

func TestWorkspaceBroadcastControlsFitMinimumWindow(t *testing.T) {
	sessions, _ := multiTestSessions(t, 3)
	if !supportsTerminalBroadcast(sessions[0].term) {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("required MyGo input hooks are missing")
		}
		t.Skip("build with Taskfile to enable the verified MyGo input hooks")
	}
	for i, s := range sessions {
		s.label = []string{"one", "two", "three"}[i]
	}
	d := &desktop{workspace: "terminal", currentSession: 1, term: sessions[0].term, sessions: sessions}
	if err := d.createTerminalGroup("project", []int{1, 2, 3}, 1, false); err != nil {
		t.Fatal(err)
	}
	d.toggleGroupBroadcast(d.terminalGroupFor(1))
	view := ui.NewTester(d.view, 640, 480)
	for _, label := range []string{"移出广播：one · 会话 1", "移出广播：two · 会话 2", "移出广播：three · 会话 3"} {
		r, ok := view.Find(label)
		if !ok || r.W < 20 || r.X < 0 || r.X+r.W > 640 || r.Y < 0 || r.Y+r.H > 480 {
			t.Fatalf("focus control %s unavailable: %+v", label, r)
		}
	}
	if err := view.Click("分屏查看"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"移出广播：one · 会话 1", "移出广播：two · 会话 2", "移出广播：three · 会话 3"} {
		if r, ok := view.Find(label); !ok || r.W < 20 || r.X < 0 || r.X+r.W > 640 {
			t.Fatalf("grid control %s unavailable: %+v", label, r)
		}
	}
}

func TestWorkspaceBroadcastCanStartWithDisconnectedMembersExcluded(t *testing.T) {
	sessions, _ := multiTestSessions(t, 3)
	if !supportsTerminalBroadcast(sessions[0].term) {
		if os.Getenv("NEKOSSH_REQUIRE_BROADCAST") == "1" {
			t.Fatal("required MyGo input hooks are missing")
		}
		t.Skip("build with Taskfile to enable the verified MyGo input hooks")
	}
	d := &desktop{workspace: "terminal", currentSession: 1, term: sessions[0].term, sessions: sessions}
	if err := d.createTerminalGroup("project", []int{1, 2, 3}, 1, false); err != nil {
		t.Fatal(err)
	}
	_ = sessions[2].term.Close()
	group := d.terminalGroupFor(1)
	d.toggleGroupBroadcast(group)
	if !group.broadcast || len(group.broadcastMembers) != 2 || group.broadcastMembers[3] {
		t.Fatal("available subset was not selected")
	}
}
