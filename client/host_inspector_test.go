package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestHostInspectorKeepsListVisibleAndEditsInPlace(t *testing.T) {
	d := &desktop{workspace: "connections", profiles: []profile{{ID: "1", Label: "生产机", Group: "工作", Settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}}}
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("工作 / 生产机"); err != nil {
		t.Fatal(err)
	}
	if d.editConnection || d.selectedProfile != "1" || d.profileInspectorAt.IsZero() {
		t.Fatal("single click did not select the host while preserving double-click handling")
	}
	d.profileInspectorAt = time.Now().Add(-time.Millisecond)
	view.Frame()
	if !d.editConnection || d.settings.Host != "one.test" || !view.HasText("主机详情面板") || !view.HasText("主机地址") || !view.HasText("连接列表") {
		t.Fatal("a selected host should open its editable details beside the list")
	}
	button, ok := view.Find("确认指纹并连接")
	if !ok || button.Y+button.H > 900 {
		t.Fatalf("connection action must remain visible below the scrolling form: %+v", button)
	}
}

func TestHostInspectorPreservesUnsavedDraft(t *testing.T) {
	first := profile{ID: "1", Label: "one", Settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	second := profile{ID: "2", Label: "two", Settings: connectionSettings{Host: "two.test", Port: "22", User: "bob"}}
	d := &desktop{profiles: []profile{first, second}}
	d.loadProfile(first)
	d.settings.Host = "draft.test"
	d.settings.Password = "temporary-test-secret"
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("two"); err != nil {
		t.Fatal(err)
	}
	if d.settings.Host != "draft.test" || d.selectedProfile != "1" || !strings.Contains(d.status, "请先保存或关闭") {
		t.Fatal("selecting another card discarded an unfinished editor")
	}
	if err := view.Click("返回连接列表"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || !d.confirmDiscardConnection {
		t.Fatal("closing a changed editor did not request confirmation")
	}
	if err := view.Click("返回连接列表"); err != nil {
		t.Fatal(err)
	}
	if d.editConnection || d.connectionDraftSet || d.settings.Password != "" {
		t.Fatal("confirmed discard did not close the editor")
	}
}

func TestHostInspectorSwitchesCleanDraftOnSelection(t *testing.T) {
	first := profile{ID: "1", Label: "one", Settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	second := profile{ID: "2", Label: "two", Settings: connectionSettings{Host: "two.test", Port: "22", User: "bob"}}
	d := &desktop{profiles: []profile{first, second}}
	d.loadProfile(first)
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("two"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || d.selectedProfile != "2" || d.settings.Host != "two.test" || d.hasUnsavedConnection() {
		t.Fatal("selecting another host did not switch the clean editor")
	}
}

func TestHostInspectorNarrowWindowUsesFullWidthEditor(t *testing.T) {
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	view := ui.NewTester(d.view, 640, 480)
	if view.HasText("主机详情面板") || !view.HasText("主机地址") || !view.HasText("返回连接列表") {
		t.Fatal("narrow window should use the full-width editor")
	}
}
