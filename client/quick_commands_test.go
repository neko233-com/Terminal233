package main

import (
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

func TestQuickCommandsPersistAndRejectStaleEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	a := &desktop{quickPath: path, quickName: "磁盘", quickText: "df -h"}
	a.saveQuickCommand()
	if len(a.quickCommands) != 1 {
		t.Fatal(a.quickStatus)
	}
	b := &desktop{quickPath: path}
	b.reloadQuickCommands()
	old := b.quickCommands[0]
	b.quickEdit, b.quickName, b.quickText = &old, "旧编辑", "echo old"
	current := a.quickCommands[0]
	a.quickEdit, a.quickName, a.quickText = &current, "新编辑", "echo new"
	a.saveQuickCommand()
	b.saveQuickCommand()
	if !strings.Contains(b.quickStatus, "其他窗口修改") {
		t.Fatal("stale edit was not rejected")
	}
	b.quickEdit, b.quickName, b.quickText = nil, "第二条", "pwd"
	b.saveQuickCommand()
	items, err := readQuickCommands(path)
	if err != nil || len(items) != 2 || items[0].Command != "echo new" || items[1].Command != "pwd" {
		t.Fatalf("independent changes were lost: %#v %v", items, err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	a.quickEdit, a.quickName, a.quickText = nil, "third", "pwd"
	a.saveQuickCommand()
	data, _ := os.ReadFile(path)
	if string(data) != "invalid" {
		t.Fatal("invalid existing file was overwritten")
	}
}

func TestQuickCommandSendsOnlyToSelectedTerminal(t *testing.T) {
	a, aPeer := net.Pipe()
	defer aPeer.Close()
	b, bPeer := net.Pipe()
	defer bPeer.Close()
	first, err := terminal.New(terminal.Options{Conn: a, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := terminal.New(terminal.Options{Conn: b, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := &desktop{workspace: "terminal", currentSession: 2, term: second, sessions: []*workspaceSession{{id: 1, term: first}, {id: 2, term: second}}}
	item := quickCommand{Name: "查看", Command: "pwd"}
	d.sendQuickCommand(item, true)
	_ = bPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(bPeer, buf); err != nil || string(buf) != "pwd\r" {
		t.Fatalf("selected terminal received %q: %v", buf, err)
	}
	_ = aPeer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := aPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("command was sent to another terminal")
	}
	d.sendQuickCommand(quickCommand{Name: "bad", Command: "pwd\nexit"}, true)
	if !strings.Contains(d.quickStatus, "有效") {
		t.Fatal("multiline command accepted")
	}
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("快速命令"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("单行命令") || !view.HasText("快速命令保存在本机明文文件中；请勿保存密码或令牌。发送目标为当前选中的会话，发送并回车会与当前输入行一起执行。 ") {
		t.Fatal("quick command editor or plaintext warning missing")
	}
}
