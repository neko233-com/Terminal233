package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsPanelFitsSmallWindowAndKeepsUpdatesReachable(t *testing.T) {
	d := &desktop{updateAvailable: &updateCandidate{Version: "1.2.3"}}
	view := ui.NewTester(d.view, 640, 480)
	if err := view.Click("设置（有更新）"); err != nil {
		t.Fatal(err)
	}
	panel, ok := view.Find("设置面板")
	if !ok || panel.X < 0 || panel.Y < 0 || panel.X+panel.W > 640 || panel.Y+panel.H > 480 {
		t.Fatalf("settings panel is not reachable: %+v", panel)
	}
	view.Scroll(panel.X+panel.W/2, panel.Y+panel.H/2, 0, 600)
	button, ok := view.Find("下载安装包…")
	if !ok || button.Y < panel.Y || button.Y+button.H > panel.Y+panel.H {
		t.Fatalf("update action is not reachable by scrolling: panel=%+v button=%+v", panel, button)
	}
	view.Key(0, ui.KeyEscape)
	if d.showSettings {
		t.Fatal("Escape did not close settings")
	}
}

func TestSettingsButtonKeepsUpdateStateVisible(t *testing.T) {
	d := &desktop{}
	if d.settingsButtonLabel() != "设置" {
		t.Fatal("unexpected default label")
	}
	d.updateStatus = "检查更新失败：test"
	if d.settingsButtonLabel() != "设置（更新出错）" {
		t.Fatal("update error was hidden")
	}
	d.updateAvailable = &updateCandidate{Version: "1.2.3"}
	if d.settingsButtonLabel() != "设置（有更新）" {
		t.Fatal("available update was hidden")
	}
	d.updateDownloading = true
	if d.settingsButtonLabel() != "设置（正在下载更新）" {
		t.Fatal("ongoing download was hidden")
	}
}
