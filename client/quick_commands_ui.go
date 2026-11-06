package main

import (
	"errors"
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) reloadQuickCommands() {
	if d.quickPath == "" {
		d.quickStatus = "快速命令存储不可用"
		return
	}
	items, err := readQuickCommands(d.quickPath)
	if err != nil {
		d.quickStatus = "读取失败，原文件未覆盖：" + err.Error()
		return
	}
	d.quickCommands, d.quickEdit, d.quickConfirm = items, nil, ""
	d.quickName, d.quickText = "", ""
	d.quickStatus = "快速命令已刷新"
}

func (d *desktop) saveQuickCommand() bool {
	item, err := newQuickCommand(d.quickName, d.quickText)
	if err != nil {
		d.quickStatus = err.Error()
		return false
	}
	expected := d.quickEdit
	items, err := updateQuickCommands(d.quickPath, func(items []quickCommand) ([]quickCommand, error) {
		if expected == nil {
			if len(items) >= maxQuickCommands {
				return nil, errors.New("快速命令已达到 100 条")
			}
			return append(items, item), nil
		}
		for i, current := range items {
			if current.ID == expected.ID {
				if current != *expected {
					return nil, errors.New("命令已在其他窗口修改，请刷新后重试")
				}
				item.ID = current.ID
				items[i] = item
				return items, nil
			}
		}
		return nil, errors.New("命令已被删除，请刷新后重试")
	})
	if err != nil {
		d.quickStatus = "保存失败：" + err.Error()
		return false
	}
	d.quickCommands, d.quickEdit, d.quickConfirm = items, nil, ""
	d.quickName, d.quickText = "", ""
	d.quickStatus = "快速命令已保存到本机"
	return true
}

func (d *desktop) deleteQuickCommand(expected quickCommand) {
	if d.quickConfirm != expected.ID {
		d.quickConfirm = expected.ID
		d.quickStatus = "再次点击确认删除：" + expected.Name
		return
	}
	items, err := updateQuickCommands(d.quickPath, func(items []quickCommand) ([]quickCommand, error) {
		for i, current := range items {
			if current.ID == expected.ID {
				if current != expected {
					return nil, errors.New("命令已在其他窗口修改，请刷新后重试")
				}
				return append(items[:i], items[i+1:]...), nil
			}
		}
		return nil, errors.New("命令已被删除，请刷新后重试")
	})
	if err != nil {
		d.quickStatus, d.quickConfirm = "删除失败："+err.Error(), ""
		return
	}
	d.quickCommands, d.quickEdit, d.quickConfirm = items, nil, ""
	d.quickName, d.quickText, d.quickStatus = "", "", "快速命令已删除"
}

func (d *desktop) sendQuickCommand(item quickCommand, execute bool) {
	if d.term == nil || terminalEnded(d.term) || validateQuickCommand(item.Name, item.Command) != nil {
		d.quickStatus = "请先选择有效的终端会话和命令"
		return
	}
	target := d.quickCommandTarget()
	if execute {
		d.term.Send([]byte(item.Command + "\r"))
		d.quickStatus = "已向「" + target + "」提交命令并回车：" + item.Name
	} else {
		d.term.Paste(item.Command)
		d.quickStatus = "已插入「" + target + "」，请检查后执行：" + item.Name
	}
}

func (d *desktop) quickCommandTarget() string {
	for _, session := range d.sessions {
		if session.id == d.currentSession && session.term == d.term && d.term != nil {
			return fmt.Sprintf("%s（会话 %d）", session.label, session.id)
		}
	}
	if d.term != nil {
		return "当前终端"
	}
	return "未选择终端"
}

func (d *desktop) quickCommandsView(c *ui.Context) {
	ui.Column(c).FillWidth().Padding(10, 12).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Text(c, "命令目标："+d.quickCommandTarget()).Bold().FontSize(12).Wrap()
		ui.Text(c, "快速命令保存在本机明文文件中；请勿保存密码或令牌。发送目标为当前选中的会话，发送并回车会与当前输入行一起执行。 ").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			ui.TextInput(c.Key("quick-name"), &d.quickName).Label("命令名称").Placeholder("例如查看磁盘").Grow(1)
			ui.TextInput(c.Key("quick-text"), &d.quickText).Label("单行命令").Placeholder("例如 df -h").Grow(2)
			if ui.Button(c.Key("quick-save"), "保存命令").Clicked() {
				d.saveQuickCommand()
			}
			if d.quickEdit != nil && ui.Button(c.Key("quick-cancel-edit"), "取消编辑").Clicked() {
				d.quickEdit, d.quickName, d.quickText = nil, "", ""
			}
			if ui.Button(c.Key("quick-refresh"), "刷新").Clicked() {
				d.reloadQuickCommands()
			}
		})
		if d.quickStatus != "" {
			ui.Text(c, d.quickStatus).FontSize(12).Wrap()
		}
		if len(d.quickCommands) > 0 {
			ui.TextInput(c.Key("quick-filter"), &d.quickFilter).Label("搜索快速命令").Placeholder("按名称或命令内容搜索")
			visible := filterQuickCommands(d.quickCommands, d.quickFilter)
			if len(visible) == 0 {
				ui.Text(c, "没有匹配的快速命令").FontSize(12).TextColor(c.Theme().TextMuted)
			}
			if len(visible) == 0 {
				return
			}
			ui.Scroll(c.Key("quick-list")).Height(150).FillWidth().Children(func() {
				for _, command := range visible {
					item := command
					ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("%s · %s", item.Name, item.Command)).Grow(1).Wrap()
						if ui.Button(c.Key("quick-insert-"+item.ID), "插入").Disabled(terminalEnded(d.term)).Clicked() {
							d.sendQuickCommand(item, false)
						}
						if ui.Button(c.Key("quick-run-"+item.ID), "发送并回车").Disabled(terminalEnded(d.term)).Clicked() {
							d.sendQuickCommand(item, true)
						}
						if ui.Button(c.Key("quick-edit-"+item.ID), "编辑").Clicked() {
							d.quickEdit, d.quickName, d.quickText, d.quickConfirm = &item, item.Name, item.Command, ""
						}
						label := "删除"
						if d.quickConfirm == item.ID {
							label = "确认删除"
						}
						if ui.Button(c.Key("quick-delete-"+item.ID), label).Clicked() {
							d.deleteQuickCommand(item)
						}
					})
				}
			})
		}
	})
}

// The wide terminal uses a side panel: the list stays in view and editing is
// opened explicitly, leaving the terminal itself at full height.
func (d *desktop) quickCommandsSideView(c *ui.Context) {
	ui.Column(c).FillWidth().Padding(14).Gap(10).Children(func() {
		ui.Text(c, "快速命令").FontSize(16).Bold()
		ui.Text(c, "命令目标："+d.quickCommandTarget()).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			if ui.Button(c.Key("quick-new"), "+ 新建命令").Disabled(d.quickEditorOpen).Clicked() {
				d.quickEditorOpen = true
				d.quickEdit, d.quickName, d.quickText, d.quickConfirm = nil, "", "", ""
			}
			if ui.Button(c.Key("quick-refresh"), "刷新").Disabled(d.quickEditorOpen).Clicked() {
				d.reloadQuickCommands()
			}
		})
		if d.quickEditorOpen {
			ui.Text(c, "命令保存在本机明文文件中，请勿填写密码或令牌。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			ui.TextInput(c.Key("quick-name"), &d.quickName).Label("命令名称").Placeholder("命令名称").FillWidth()
			ui.TextInput(c.Key("quick-text"), &d.quickText).Label("单行命令").Placeholder("单行命令").FillWidth()
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				if ui.PrimaryButton(c.Key("quick-save"), "保存命令").Clicked() && d.saveQuickCommand() {
					d.quickEditorOpen = false
				}
				if ui.Button(c.Key("quick-cancel-edit"), "取消编辑").Clicked() {
					d.quickEditorOpen = false
					d.quickEdit, d.quickName, d.quickText = nil, "", ""
				}
			})
		}
		if d.quickStatus != "" {
			ui.Text(c, d.quickStatus).FontSize(12).Wrap()
		}
		if len(d.quickCommands) == 0 {
			ui.Text(c, "还没有快速命令").FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		ui.TextInput(c.Key("quick-filter"), &d.quickFilter).Label("搜索快速命令").Placeholder("搜索命令").FillWidth()
		visible := filterQuickCommands(d.quickCommands, d.quickFilter)
		if len(visible) == 0 {
			ui.Text(c, "没有匹配的快速命令").FontSize(12).TextColor(c.Theme().TextMuted)
		}
		for _, command := range visible {
			item := command
			ui.Column(c).FillWidth().Padding(10).Gap(6).Background(c.Theme().Background).Radius(8).Children(func() {
				ui.Text(c, item.Name).Bold().Wrap()
				ui.Text(c, item.Command).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					if ui.Button(c.Key("quick-insert-"+item.ID), "插入").Disabled(terminalEnded(d.term)).Clicked() {
						d.sendQuickCommand(item, false)
					}
					if ui.Button(c.Key("quick-run-"+item.ID), "发送并回车").Disabled(terminalEnded(d.term)).Clicked() {
						d.sendQuickCommand(item, true)
					}
					if ui.Button(c.Key("quick-edit-"+item.ID), "编辑").Disabled(d.quickEditorOpen).Clicked() {
						d.quickEditorOpen = true
						d.quickEdit, d.quickName, d.quickText, d.quickConfirm = &item, item.Name, item.Command, ""
					}
					label := "删除"
					if d.quickConfirm == item.ID {
						label = "确认删除"
					}
					if ui.Button(c.Key("quick-delete-"+item.ID), label).Disabled(d.quickEditorOpen).Clicked() {
						d.deleteQuickCommand(item)
					}
				})
			})
		}
	})
}
