package main

import "github.com/egoist/mygo/ui"

func (d *desktop) terminalMainView(c *ui.Context, width float32, compact bool) {
	if !d.showFilePanel || d.currentSerialSession() != nil {
		ui.Column(c).Grow(1).FillWidth().Children(func() { d.terminalWorkspace(c, width) })
		return
	}
	files := func() {
		ui.Scroll(c.Key("terminal-file-panel")).Label("终端文件面板").Fill().Children(func() {
			ui.Column(c).Padding(14).Gap(8).Children(func() { d.fileView(c, true) })
		})
	}
	if width < 940 {
		container := ui.Column(c.Key("terminal-file-stack")).Grow(1).FillWidth()
		height := container.Bounds().H
		if d.terminalFileStackPercent == 0 {
			d.terminalFileStackPercent = 65
			if compact {
				d.terminalFileStackPercent = 75
			}
		}
		d.terminalFileStackPercent = fitPanePercent(d.terminalFileStackPercent, height, 80, 60)
		container.Children(func() {
			split := ui.SplitVerticalPercent(c.Key("terminal-file-split-vertical"), &d.terminalFileStackPercent,
				func() { ui.Column(c).Fill().Children(func() { d.terminalWorkspace(c, width) }) }, files,
			).Label("终端与文件上下分隔").Fill()
			if split.Changed() {
				d.terminalFileStackPercent = fitPanePercent(d.terminalFileStackPercent, height, 80, 60)
				c.Invalidate()
			}
		})
		return
	}
	if d.terminalFilePercent == 0 {
		d.terminalFilePercent = 60
	}
	d.terminalFilePercent = fitPanePercent(d.terminalFilePercent, width, 240, 280)
	split := ui.SplitPercent(c.Key("terminal-file-split"), &d.terminalFilePercent,
		func() {
			ui.Column(c).Fill().Children(func() { d.terminalWorkspace(c, (width-1)*d.terminalFilePercent/100) })
		}, files,
	).Label("终端与文件左右分隔").Grow(1).FillWidth()
	if split.Changed() {
		d.terminalFilePercent = fitPanePercent(d.terminalFilePercent, width, 240, 280)
		c.Invalidate()
	}
}
