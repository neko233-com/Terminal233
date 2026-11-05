package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func (d *desktop) closeTerminalSearch() {
	if d.term != nil {
		d.term.EndSearch()
	}
	d.terminalSearchOpen, d.terminalSearchText = false, ""
}

func terminalSearchStatus(s terminal.SearchStatus) string {
	if s.Text == "" {
		return "输入要查找的文本"
	}
	if s.Searching {
		return fmt.Sprintf("正在查找 · %d/%d", s.Selected, s.Total)
	}
	if s.Total == 0 {
		return "没有匹配结果"
	}
	return fmt.Sprintf("%d/%d", s.Selected, s.Total)
}

func (d *desktop) terminalSearchView(c *ui.Context) {
	if d.term == nil {
		return
	}
	status := d.term.SearchStatus()
	if status.Searching {
		// The terminal searches scrollback incrementally in its render frames.
		c.After(100 * time.Millisecond)
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).Padding(8, 12).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("terminal-search-input"), &d.terminalSearchText).
			Label("查找终端输出").Placeholder("查找屏幕和滚动历史").Grow(1).
			OnChange(func() { d.term.Search(d.terminalSearchText) }).
			OnSubmit(func() { d.term.SearchNext() })
		ui.Text(c, terminalSearchStatus(status)).FontSize(12).TextColor(c.Theme().TextMuted)
		if ui.Button(c.Key("terminal-search-previous"), "上一处").Disabled(status.Total == 0).Clicked() {
			d.term.SearchPrevious()
		}
		if ui.Button(c.Key("terminal-search-next"), "下一处").Disabled(status.Total == 0).Clicked() {
			d.term.SearchNext()
		}
		if ui.Button(c.Key("terminal-search-close"), "关闭查找").Clicked() {
			d.closeTerminalSearch()
		}
	})
}
