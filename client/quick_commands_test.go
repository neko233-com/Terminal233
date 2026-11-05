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
	d := &desktop{workspace: "terminal", currentSession: 2, term: second, sessions: []*workspaceSession{{id: 1, label: "相同名称", term: first}, {id: 2, label: "相同名称", term: second}}}
	item := quickCommand{Name: "查看", Command: "pwd"}
	d.sendQuickCommand(item, true)
	if !strings.Contains(d.quickStatus, "相同名称（会话 2）") {
		t.Fatal("result did not identify the target")
	}
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
	if !view.HasText("命令目标：相同名称（会话 2）") {
		t.Fatal("selected target is not displayed")
	}
	d.activateSession(1)
	view.Frame()
	if !view.HasText("命令目标：相同名称（会话 1）") {
		t.Fatal("target did not follow the active session")
	}
}

func TestQuickCommandSearchFiltersByNameAndContent(t *testing.T) {
	items := []quickCommand{{ID: "one", Name: "磁盘", Command: "df -h"}, {ID: "two", Name: "日志", Command: "journalctl -xe"}}
	if got := filterQuickCommands(items, " DF "); len(got) != 1 || got[0].Name != "磁盘" {
		t.Fatalf("content filter failed: %#v", got)
	}
	if got := filterQuickCommands(items, "日志"); len(got) != 1 || got[0].Command != "journalctl -xe" {
		t.Fatalf("name filter failed: %#v", got)
	}
	if got := filterQuickCommands(items, "absent"); len(got) != 0 {
		t.Fatal("unknown query returned a result")
	}
	if len(items) != 2 {
		t.Fatal("filter changed the saved list")
	}
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, quickOpen: true, quickCommands: items}
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("搜索快速命令"); err != nil {
		t.Fatal(err)
	}
	view.Type("journal")
	if !view.HasText("日志 · journalctl -xe") || view.HasText("磁盘 · df -h") {
		t.Fatal("search input did not filter the command list")
	}
}

func TestQuickCommandReachesVerifiedSSHSession(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, term, err := openSSHSession(settings, terminal.Options{NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{term: term, shell: shell}
	d.sendQuickCommand(quickCommand{Name: "测试", Command: "echo NEKO_QUICK_SSH"}, true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(term.Text(), "echo NEKO_QUICK_SSH") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("SSH test shell did not echo the command: %q", term.Text())
}
