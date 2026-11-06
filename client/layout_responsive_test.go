package main

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestCompactConnectionListKeepsFirstConnectionVisible(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{{ID: "1", Label: "small-window-test", Settings: connectionSettings{Host: "example.test", Port: "22", User: "alice"}}}}
	view := ui.NewTester(d.view, 640, 480)
	row, ok := view.Find("small-window-test · example.test:22")
	if !ok || row.Y+row.H > 480 {
		t.Fatalf("first connection not visible in compact window: %+v", row)
	}
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("名称排序") || !view.HasText("勾选当前显示的本机连接") {
		t.Fatal("compact tools could not be expanded")
	}
}

func TestHostSearchPrecedesToolbarAtDesktopAndNarrowSizes(t *testing.T) {
	for _, size := range [][2]int{{1280, 800}, {640, 480}} {
		d := &desktop{workspace: "connections"}
		view := ui.NewTester(d.view, size[0], size[1])
		search, searchOK := view.Find("连接列表：搜索连接或快速连接")
		create, createOK := view.Find("+ 新建连接")
		if !searchOK || !createOK || search.Y+search.H > create.Y || create.Y+create.H > float32(size[1]) {
			t.Errorf("search or toolbar inaccessible at %dx%d: search=%+v create=%+v", size[0], size[1], search, create)
		}
	}
}

func TestTerminalRemainsVisibleWithPanelsAtMinimumWindow(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}, quickOpen: true, terminalSearchOpen: true, showFilePanel: true, quickCommands: []quickCommand{{ID: "one", Name: "test", Command: "pwd"}}}
	for _, size := range [][2]int{{640, 480}, {640, 600}, {640, 650}, {640, 800}, {640, 1000}, {900, 700}, {1200, 760}} {
		view := ui.NewTester(d.view, size[0], size[1])
		r, ok := view.Find("Terminal")
		if !ok || r.H < 80 || r.Y < 0 || r.Y+r.H > float32(size[1]) {
			t.Errorf("terminal unusable at %dx%d: found=%v rect=%+v", size[0], size[1], ok, r)
		}
	}
	d.shell = &remoteShell{}
	remoteView := ui.NewTester(d.view, 640, 480)
	if r, ok := remoteView.Find("Terminal"); !ok || r.H < 80 {
		t.Fatalf("SSH terminal unusable with monitor control: %+v", r)
	}
	d.monitorCancel = func() {}
	d.monitorStatus = "运行时间 2 小时 · 1 分钟负载 1.25 · 内存 5.0 / 8.0 GiB"
	remoteView.Frame()
	if r, ok := remoteView.Find("Terminal"); !ok || r.H < 80 {
		t.Fatalf("SSH terminal unusable with monitor panel: %+v", r)
	}
	d.monitorCancel = nil
	d.shell = nil
	view := ui.NewTester(d.view, 640, 480)
	panel, ok := view.Find("快速命令面板")
	if !ok || panel.H != 90 {
		t.Fatalf("compact quick-command panel not found: %+v", panel)
	}
	view.Scroll(panel.X+panel.W/2, panel.Y+panel.H/2, 0, 350)
	filter, ok := view.Find("搜索快速命令")
	if !ok || filter.Y+filter.H/2 < panel.Y || filter.Y+filter.H/2 > panel.Y+panel.H {
		t.Fatalf("quick-command controls cannot be reached by scrolling: panel=%+v filter=%+v", panel, filter)
	}
}

func TestWideTerminalPlacesQuickCommandsBesideTerminal(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}, quickOpen: true}
	view := ui.NewTester(d.view, 1280, 800)
	terminalRect, terminalOK := view.Find("Terminal")
	panelRect, panelOK := view.Find("快速命令面板")
	if !terminalOK || !panelOK || terminalRect.W < 300 || terminalRect.H < 200 || panelRect.X < terminalRect.X+terminalRect.W-1 {
		t.Fatalf("quick commands should be beside a usable terminal: terminal=%+v panel=%+v", terminalRect, panelRect)
	}
	if err := view.Click("打开文件面板"); err != nil {
		t.Fatal(err)
	}
	if d.quickOpen || !d.showFilePanel {
		t.Fatal("opening files should replace the quick commands panel")
	}
	if err := view.Click("快速命令"); err != nil {
		t.Fatal(err)
	}
	if !d.quickOpen || d.showFilePanel {
		t.Fatal("opening quick commands should replace the file panel")
	}
}

func TestTerminalToolbarUsesAccessibleCompactActions(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	for _, size := range [][2]int{{640, 480}, {1280, 800}} {
		d := &desktop{workspace: "terminal", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}}
		view := ui.NewTester(d.view, size[0], size[1])
		for _, label := range []string{"查找", "快速命令", "分屏查看", "打开文件面板"} {
			r, ok := view.Find(label)
			if !ok || r.W > 40 || r.H > 40 || r.X < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
				t.Errorf("toolbar action %q not compact or reachable at %dx%d: %+v", label, size[0], size[1], r)
			}
		}
	}
}

func TestTerminalFocusLayoutKeepsNavigationAndPreference(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	for _, size := range [][2]int{{640, 480}, {1280, 800}} {
		d := &desktop{workspace: "terminal", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}}
		view := ui.NewTester(d.view, size[0], size[1])
		terminalRect, ok := view.Find("Terminal")
		if !ok || terminalRect.X > 1 || view.HasText("工作区") {
			t.Fatalf("terminal should start with the sidebar hidden: %+v", terminalRect)
		}
		if err := view.Click("展开侧栏"); err != nil {
			t.Fatal(err)
		}
		if !view.HasText("工作区") || d.sidebarCollapsed || !d.terminalNavigationOpen {
			t.Fatal("terminal navigation did not open independently of the saved sidebar preference")
		}
		if err := view.Click("收起侧栏"); err != nil {
			t.Fatal(err)
		}
		if d.sidebarCollapsed || d.terminalNavigationOpen || view.HasText("工作区") {
			t.Fatal("closing terminal navigation changed the saved sidebar preference")
		}
		if err := view.Click("主机"); err != nil {
			t.Fatal(err)
		}
		if d.workspace != "connections" || !view.HasText("工作区") {
			t.Fatal("hosts tab did not restore normal navigation")
		}
	}
}

func TestWideWorkspaceTabsSwitchBetweenHostsAndSession(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "connections", term: term, currentSession: 1, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}}
	view := ui.NewTester(d.view, 1280, 800)
	if err := view.Click("local"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "terminal" || !view.Focused("Terminal") {
		t.Fatal("session tab should open and focus the terminal")
	}
	if err := view.Click("主机"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "connections" {
		t.Fatal("hosts tab should return to the host workspace")
	}
}

func TestTerminalFilePanelResizesByPointerAndKeyboard(t *testing.T) {
	left, peer := net.Pipe()
	defer peer.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1, showFilePanel: true, sessions: []*workspaceSession{{id: 1, label: "local", term: term}}}
	view := ui.NewTester(d.view, 1500, 900)
	panel, ok := view.Find("终端文件面板")
	if !ok || panel.W < 200 {
		t.Fatalf("file panel missing or too narrow: %+v", panel)
	}
	x, y := panel.X, panel.Y+panel.H/2
	view.Press(x, y)
	view.Move(x+80, y)
	view.Release(x+80, y)
	if d.terminalFilePercent <= 60 {
		t.Fatalf("file divider did not move: %v", d.terminalFilePercent)
	}
	before := d.terminalFilePercent
	view.Key(0, ui.KeyRight)
	if d.terminalFilePercent <= before {
		t.Fatal("file divider did not respond to keyboard")
	}
	d.showFilePanel = false
	view.Frame()
	d.showFilePanel = true
	view.Frame()
	if d.terminalFilePercent <= before {
		t.Fatal("file panel size was lost when hidden")
	}
}

func TestSplitLayoutChoiceAndPersistence(t *testing.T) {
	firstConn, firstPeer := net.Pipe()
	defer firstPeer.Close()
	secondConn, secondPeer := net.Pipe()
	defer secondPeer.Close()
	first, err := terminal.New(terminal.Options{Conn: firstConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := terminal.New(terminal.Options{Conn: secondConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := &desktop{workspace: "terminal", term: first, currentSession: 1, splitSession: 2, splitFirstSession: 1, sessions: []*workspaceSession{{id: 1, label: "first", term: first}, {id: 2, label: "second", term: second}}}
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := d.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1500, 900)
	for _, tc := range []struct {
		label, id string
		stacked   bool
	}{{"上下分屏", "stacked", true}, {"左右分屏", "side", false}} {
		if err := view.Click("设置"); err != nil {
			t.Fatal(err)
		}
		if err := view.Click("选择分屏布局"); err != nil {
			t.Fatal(err)
		}
		if err := view.Click(tc.label); err != nil {
			t.Fatal(err)
		}
		view.Key(0, ui.KeyEscape)
		a, oka := view.Find("终端：first")
		b, okb := view.Find("终端：second")
		if !oka || !okb || d.splitLayout != tc.id {
			t.Fatalf("layout %s not applied", tc.label)
		}
		if tc.stacked && b.Y < a.Y+a.H-1 {
			t.Fatalf("panes not stacked: %+v %+v", a, b)
		}
		if !tc.stacked && b.X < a.X+a.W-1 {
			t.Fatalf("panes not side by side: %+v %+v", a, b)
		}
		if tc.stacked {
			x, y := a.X+a.W/2, a.Y+a.H
			view.Press(x, y)
			view.Move(x, y+45)
			view.Release(x, y+45)
			if d.splitVerticalPercent <= 50 {
				t.Fatalf("vertical divider did not resize: %v", d.splitVerticalPercent)
			}
			before := d.splitVerticalPercent
			view.Key(0, ui.KeyDown)
			if d.splitVerticalPercent <= before {
				t.Fatal("vertical divider keyboard resize failed")
			}
		} else {
			x, y := a.X+a.W, a.Y+a.H/2
			view.Press(x, y)
			view.Move(x+45, y)
			view.Release(x+45, y)
			if d.splitHorizontalPercent <= 50 {
				t.Fatalf("horizontal divider did not resize: %v", d.splitHorizontalPercent)
			}
			before := d.splitHorizontalPercent
			view.Key(0, ui.KeyRight)
			if d.splitHorizontalPercent <= before {
				t.Fatal("horizontal divider keyboard resize failed")
			}
		}
		restored := &desktop{}
		if err := restored.loadPreferences(path); err != nil || restored.splitLayout != tc.id {
			t.Fatalf("layout not persisted: %v", err)
		}
	}
}

func TestSplitTerminalsRemainVisibleWithPanels(t *testing.T) {
	firstConn, firstPeer := net.Pipe()
	defer firstPeer.Close()
	secondConn, secondPeer := net.Pipe()
	defer secondPeer.Close()
	first, err := terminal.New(terminal.Options{Conn: firstConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := terminal.New(terminal.Options{Conn: secondConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := &desktop{workspace: "terminal", term: first, currentSession: 1, splitSession: 2, splitFirstSession: 1, sessions: []*workspaceSession{{id: 1, label: "first", term: first}, {id: 2, label: "second", term: second}}, quickOpen: true, terminalSearchOpen: true, showFilePanel: true, monitorCancel: func() {}, monitorStatus: "运行时间 2 小时 · 1 分钟负载 1.25 · 内存 5.0 / 8.0 GiB · 根磁盘 3.0 / 10.0 GiB · CPU 50% · 网卡合计 ↓1.0 ↑2.0 KiB/s", monitorData: monitorDisplay{cpu: .5, memory: .625, disk: .3, hasCPU: true, hasMemory: true, hasDisk: true}, quickCommands: []quickCommand{{ID: "one", Name: "test", Command: "pwd"}}}
	for _, size := range [][2]int{{640, 480}, {900, 650}, {900, 700}, {940, 480}, {1024, 480}, {1200, 480}, {1200, 760}, {1200, 800}, {1500, 800}, {1500, 900}} {
		view := ui.NewTester(d.view, size[0], size[1])
		for _, label := range []string{"终端：first", "终端：second"} {
			r, ok := view.Find(label)
			if !ok || r.H < 40 || r.W < 100 || r.X < 0 || r.X+r.W > float32(size[0]) || r.Y < 0 || r.Y+r.H > float32(size[1]) {
				t.Errorf("%s unusable at %dx%d: found=%v rect=%+v", label, size[0], size[1], ok, r)
			}
		}
		if size[1] < 650 {
			a, _ := view.Find("终端：first")
			b, _ := view.Find("终端：second")
			if a.X+a.W > b.X+1 && b.X+b.W > a.X+1 {
				t.Errorf("shallow-window panes overlap at %dx%d: %+v %+v", size[0], size[1], a, b)
			}
		}
	}
}

func TestSplitSessionButtonFocusesSelectedTerminal(t *testing.T) {
	firstConn, firstPeer := net.Pipe()
	defer firstPeer.Close()
	secondConn, secondPeer := net.Pipe()
	defer secondPeer.Close()
	first, err := terminal.New(terminal.Options{Conn: firstConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := terminal.New(terminal.Options{Conn: secondConn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := &desktop{workspace: "terminal", term: first, currentSession: 1, splitSession: 2, splitFirstSession: 1, sessions: []*workspaceSession{{id: 1, label: "first", term: first}, {id: 2, label: "second", term: second}}}
	view := ui.NewTester(d.view, 1500, 900)
	if err := view.Click("终端：first"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("切换到：second"); err != nil {
		t.Fatal(err)
	}
	if d.currentSession != 2 || !view.Focused("终端：second") {
		t.Fatalf("selected session=%d, second terminal focused=%v", d.currentSession, view.Focused("终端：second"))
	}
	if err := view.Click("切换到：first"); err != nil {
		t.Fatal(err)
	}
	if d.currentSession != 1 || !view.Focused("终端：first") {
		t.Fatalf("selected session=%d, first terminal focused=%v", d.currentSession, view.Focused("终端：first"))
	}
	if err := view.Click("second"); err != nil {
		t.Fatal(err)
	}
	if d.currentSession != 2 || !view.Focused("终端：second") {
		t.Fatalf("tab selected session=%d, second terminal focused=%v", d.currentSession, view.Focused("终端：second"))
	}
	view.Key(ui.Cmd, ui.KeyTab)
	if d.currentSession != 1 || !view.Focused("终端：first") {
		t.Fatalf("next shortcut selected session=%d, first terminal focused=%v", d.currentSession, view.Focused("终端：first"))
	}
	view.Key(ui.Cmd|ui.Shift, ui.KeyTab)
	if d.currentSession != 2 || !view.Focused("终端：second") {
		t.Fatalf("previous shortcut selected session=%d, second terminal focused=%v", d.currentSession, view.Focused("终端：second"))
	}
	d.busy = true
	view.Key(ui.Cmd, ui.KeyTab)
	if d.currentSession != 2 {
		t.Fatal("shortcut switched session while an operation is busy")
	}
}

func TestSessionTabsReorderWithoutChangingActiveOrSplit(t *testing.T) {
	left, peer := net.Pipe()
	defer peer.Close()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d := &desktop{workspace: "terminal", term: term, currentSession: 1, splitSession: 2, splitFirstSession: 1, sessions: []*workspaceSession{
		{id: 1, label: "first", term: term}, {id: 2, label: "second"}, {id: 3, label: "third"},
	}}
	view := ui.NewTester(d.view, 1500, 900)
	from, ok := view.Find("third")
	if !ok {
		t.Fatal("third tab missing")
	}
	to, ok := view.Find("first")
	if !ok {
		t.Fatal("first tab missing")
	}
	view.Press(from.X+from.W/2, from.Y+from.H/2)
	view.Move(to.X+to.W/2, to.Y+to.H/2)
	view.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.sessions[0].id != 3 || d.sessions[1].id != 1 || d.sessions[2].id != 2 {
		t.Fatal("tab drag did not reorder")
	}
	if d.currentSession != 1 || d.splitSession != 2 || d.splitFirstSession != 1 {
		t.Fatal("reordering changed active or split session")
	}
	if err := view.RightClick("first"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("向右移动"); err != nil {
		t.Fatal(err)
	}
	if d.sessions[0].id != 3 || d.sessions[1].id != 2 || d.sessions[2].id != 1 {
		t.Fatal("tab menu did not reorder")
	}
	view.Key(ui.Cmd, ui.KeyTab)
	if d.currentSession != 3 {
		t.Fatal("session shortcut did not follow reordered tabs")
	}
}
