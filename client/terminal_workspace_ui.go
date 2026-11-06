package main

import "github.com/egoist/mygo/ui"

func (d *desktop) terminalMainView(c *ui.Context, width float32, compact bool) {
	if d.showFilePanel && width < 940 {
		fileHeight := float32(260)
		if compact {
			fileHeight = 75
		}
		ui.Column(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).Grow(1).FillWidth().Children(func() { d.terminalWorkspace(c, width) })
			ui.Scroll(c).Height(fileHeight).Shrink(0).FillWidth().BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
				ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
			})
		})
		return
	}
	if d.showFilePanel {
		if d.terminalFilePercent == 0 {
			d.terminalFilePercent = 60
		}
		ui.SplitPercent(c.Key("terminal-file-split"), &d.terminalFilePercent,
			func() { ui.Column(c).Fill().Children(func() { d.terminalWorkspace(c, width) }) },
			func() {
				ui.Scroll(c.Key("terminal-file-panel")).Label("终端文件面板").Fill().Children(func() {
					ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
				})
			},
		).Grow(1).FillWidth()
		return
	}
	ui.Column(c).Grow(1).FillWidth().Children(func() { d.terminalWorkspace(c, width) })
}
