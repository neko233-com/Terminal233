package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func (d *desktop) settingsButtonLabel() string {
	switch {
	case d.updateDownloading:
		return "设置（正在下载更新）"
	case d.updatePath != "":
		return "设置（更新已下载）"
	case d.updateAvailable != nil:
		return "设置（有更新）"
	case strings.HasPrefix(d.updateStatus, "检查更新失败") || strings.HasPrefix(d.updateStatus, "更新下载失败"):
		return "设置（更新出错）"
	default:
		return "设置"
	}
}

// Settings are available from every workspace without keeping a long form in
// the navigation rail. The scroll is bounded for the minimum window size.
func (d *desktop) settingsView(c *ui.Context, height float32) {
	panelHeight := height - 80
	if panelHeight > 420 {
		panelHeight = 420
	}
	if panelHeight < 250 {
		panelHeight = 250
	}
	ui.Scroll(c.Key("settings-scroll")).Label("设置面板").Width(320).Height(panelHeight).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Children(func() {
		ui.Column(c).FillWidth().Padding(14).Gap(9).Children(func() {
			ui.Text(c, "设置").FontSize(16).Bold()
			if ui.Button(c.Key("agent-cli-copy"), "复制 Agent CLI 命令").Tooltip("agent hosts 列出主机；exec、ls、get、put 复用已保存的凭据和指纹").FillWidth().Clicked() {
				if exe, err := os.Executable(); err == nil {
					mygo.Clipboard.WriteText("& '" + strings.ReplaceAll(exe, "'", "''") + "' agent help")
				}
			}
			ui.Text(c, "终端字号").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				if ui.Button(c.Key("font-smaller"), "A−").Label("缩小终端字号").Disabled(d.currentTerminalFontSize() <= minTerminalFontSize).Clicked() {
					d.changeTerminalFont(terminal.Zoom{Steps: -1})
				}
				ui.Text(c, fmt.Sprint(d.currentTerminalFontSize())).Grow(1)
				if ui.Button(c.Key("font-larger"), "A+").Label("放大终端字号").Disabled(d.currentTerminalFontSize() >= maxTerminalFontSize).Clicked() {
					d.changeTerminalFont(terminal.Zoom{Steps: 1})
				}
				if ui.Button(c.Key("font-reset"), "重置").Disabled(d.currentTerminalFontSize() == defaultTerminalFontSize).Clicked() {
					d.changeTerminalFont(terminal.Zoom{Reset: true})
				}
			})
			ui.Text(c, "终端配色").FontSize(12).TextColor(c.Theme().TextMuted)
			selectedTheme := terminalThemeLabel(d.terminalTheme)
			if ui.Select(c.Key("terminal-theme"), &selectedTheme, terminalThemeLabels()).Label("选择终端配色").FillWidth().Changed() {
				d.changeTerminalTheme(terminalThemeID(selectedTheme))
			}
			autocomplete := !d.autocompleteDisabled
			if ui.Checkbox(c.Key("terminal-autocomplete"), &autocomplete, "自动补全").Label("终端自动补全").Changed() {
				d.autocompleteDisabled = !autocomplete
				d.savePreference("autocomplete-disabled", d.autocompleteDisabled)
			}
			ui.Text(c, "终端分屏布局").FontSize(12).TextColor(c.Theme().TextMuted)
			layout := splitLayoutLabel(d.splitLayout)
			if ui.Select(c.Key("split-layout"), &layout, []string{"自动布局", "左右分屏", "上下分屏"}).Label("选择分屏布局").FillWidth().Changed() {
				d.changeSplitLayout(splitLayoutID(layout))
			}
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				if ui.Button(c.Key("save-panel-sizes"), "保存面板尺寸").Clicked() {
					d.savePanelSizes()
				}
				if ui.Button(c.Key("reset-panel-sizes"), "恢复默认面板尺寸").Clicked() {
					d.terminalFilePercent, d.splitHorizontalPercent, d.splitVerticalPercent = 0, 0, 0
					d.savePanelSizes()
				}
			})
			ui.Text(c, "外部编辑器").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, filepath.Base(d.selectedEditor())).Grow(1).SingleLine()
				if ui.Button(c.Key("choose-external-editor"), "选择…").Disabled(d.busy).Clicked() {
					d.pickExternalEditor()
				}
			})
			if ui.Button(c.Key("copy-edit-recovery-root"), "复制编辑副本所在目录").Tooltip("副本保存在临时目录中名称以 nekossh-edit- 开头的文件夹；需要时请手动清理。").Clicked() {
				mygo.Clipboard.WriteText(os.TempDir())
			}
			if ui.Button(c.Key("shortcut-help-toggle"), "快捷键").Clicked() {
				d.showShortcutHelp = !d.showShortcutHelp
			}
			if d.showShortcutHelp {
				ui.Text(c, "Ctrl+Shift+1–4 切换工作区\nCtrl+T 新建标签页\nCtrl+W 关闭当前标签页\nCtrl+Shift+T 打开本机终端\nCtrl+Shift+F 查找终端输出\nCtrl+Tab / Ctrl+Shift+Tab 切换会话\nCtrl+S 保存终端工作区\nCtrl+Alt+M 切换工作区布局\nCtrl+Alt+B 切换工作区广播").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
			}
			d.updateView(c)
		})
	})
}
