package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEmptyWorkspacesOfferConnectionNavigation(t *testing.T) {
	for _, workspace := range []string{"terminal", "files"} {
		t.Run(workspace, func(t *testing.T) {
			d := &desktop{workspace: workspace}
			view := ui.NewTester(d.view, 640, 480)
			if workspace == "files" {
				if err := view.Click("会话文件"); err != nil {
					t.Fatal(err)
				}
			}
			if !view.HasText("选择 SSH 连接") || !view.HasText("打开本机终端") {
				t.Fatal("empty workspace must offer both ways to start")
			}
			if workspace == "files" && view.HasText("本地文件路径") {
				t.Fatal("unavailable file controls should not dominate the empty workspace")
			}
			if err := view.Click("选择 SSH 连接"); err != nil {
				t.Fatal(err)
			}
			if d.workspace != "connections" || d.editConnection {
				t.Fatal("empty workspace did not navigate to the connection list")
			}
		})
	}
}

func TestCloudLoginVisibleInMinimumWindow(t *testing.T) {
	d := &desktop{workspace: "cloud", cloudURL: defaultCloudURL}
	view := ui.NewTester(d.view, 640, 480)
	for _, label := range []string{"同步服务地址", "账号", "密码"} {
		if !view.HasText(label) {
			t.Fatalf("missing persistent label %q", label)
		}
	}
	button, ok := view.Find("登录同步服务")
	if !ok || button.Y+button.H > 480 {
		t.Fatalf("login should be visible without scrolling at minimum size: %+v", button)
	}
}

func TestEmptyTerminalNavigationKeepsHostDraft(t *testing.T) {
	d := &desktop{workspace: "terminal", editConnection: true, settings: connectionSettings{Host: "before.test", Port: "22", User: "alice"}}
	d.captureConnectionDraft()
	d.settings.Host = "draft.test"
	view := ui.NewTester(d.view, 640, 480)
	if err := view.Click("选择 SSH 连接"); err != nil {
		t.Fatal(err)
	}
	if d.workspace != "connections" || !d.editConnection || d.settings.Host != "draft.test" || !d.hasUnsavedConnection() {
		t.Fatal("terminal navigation lost the unfinished host draft")
	}
}
