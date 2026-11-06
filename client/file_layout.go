package main

import "github.com/egoist/mygo/ui"

func (d *desktop) clampFileWorkspaceSplit(width float32) {
	if d.fileWorkspacePercent == 0 {
		d.fileWorkspacePercent = 50
	}
	limit := min(float32(49), float32(320)*100/max(float32(1), width-1))
	d.fileWorkspacePercent = max(limit, min(100-limit, d.fileWorkspacePercent))
}

func (d *desktop) filePaneWidth(c *ui.Context, left bool) float32 {
	width, _ := c.Size()
	width = d.workspaceContentWidth(width)
	if width >= 860 {
		percent := d.fileWorkspacePercent
		if percent == 0 {
			percent = 50
		}
		if !left {
			percent = 100 - percent
		}
		width = (width - 1) * percent / 100
	}
	return width - 24
}

func fileListRegion(c *ui.Context, key, label string, height float32) ui.Element {
	list := ui.Scroll(c.Key(key)).Label(label).FillWidth()
	if height < 0 {
		return list.Grow(1).MinHeight(60)
	}
	return list.Height(height)
}

func (d *desktop) filePickerLabelWidth(c *ui.Context, left bool) float32 {
	return max(float32(50), min(float32(130), d.filePaneWidth(c, left)-270))
}

func (d *desktop) fileToolsHeight(c *ui.Context) float32 {
	width, height := c.Size()
	height -= max(float32(52), c.TitleBar().Height)
	width = d.workspaceContentWidth(width)
	if width < 860 {
		height -= 44
	}
	if len(d.externalEdits) > 0 {
		height -= 44
	}
	// Leave the path, toolbar and a usable list visible while tools scroll.
	budget := height - 245
	if d.showFileFilter {
		budget -= 34
	}
	if d.fileStatus != "" {
		budget -= 20
	}
	if len(d.filePaneSelection.names) > 1 {
		budget -= 34
	}
	return max(float32(40), min(float32(220), budget))
}

func (d *desktop) fileColumnHeader(c *ui.Context, left bool) {
	order, descending := d.fileSort, d.fileSortDescending
	scope := "会话"
	if left {
		scope, order, descending = "左侧", d.localPane.sortOrder, d.localPane.sortDescending
		if d.secondaryPane.sessionID != 0 {
			order, descending = d.secondaryPane.sortOrder, d.secondaryPane.sortDescending
		}
	}
	if order == "" {
		order = "name"
	}
	ui.Row(c.Key("file-columns-"+scope)).Label(scope+"文件列标题").FillWidth().Height(30).Shrink(0).Gap(8).Padding(0, 8).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
		for _, column := range []struct {
			key, name string
			width     float32
		}{{"name", "名称", 0}, {"size", "大小", 80}, {"time", "修改时间", 140}} {
			if column.key == "time" && d.filePaneWidth(c, left) < 520 {
				continue
			}
			label := column.name
			if order == column.key {
				if descending {
					label += " ↓"
				} else {
					label += " ↑"
				}
			}
			button := ui.Button(c.Key("file-column-"+scope+"-"+column.key), label).Label(scope + "按" + column.name + "排序").FontSize(12).Height(28).Padding(0).BorderWidth(0).Background(ui.Transparent).TextColor(c.Theme().TextMuted).Justify(ui.Start)
			if column.width == 0 {
				button = button.Grow(1)
			} else {
				button = button.Width(column.width).Shrink(0)
			}
			if button.Clicked() {
				if left {
					d.setLeftFileSort(column.key)
				} else {
					d.setFileSort(column.key)
				}
			}
		}
	})
}

func (d *desktop) fileMetadata(c *ui.Context, file remoteFile, left bool) {
	ui.Text(c, fileSizeLabel(file)).Width(80).Shrink(0).FontSize(12).TextColor(c.Theme().TextMuted)
	if d.filePaneWidth(c, left) >= 520 {
		ui.Text(c, fileModifiedLabel(file)).Width(140).Shrink(0).FontSize(12).TextColor(c.Theme().TextMuted)
	}
}

func (d *desktop) setLeftFileSort(key string) {
	order, descending := &d.localPane.sortOrder, &d.localPane.sortDescending
	if d.secondaryPane.sessionID != 0 {
		order, descending = &d.secondaryPane.sortOrder, &d.secondaryPane.sortDescending
	}
	if *order == key || (*order == "" && key == "name") {
		*order, *descending = key, !*descending
	} else {
		*order, *descending = key, false
	}
}
