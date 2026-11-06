package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
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
		d.term.Send([]byte(strings.ReplaceAll(strings.TrimSuffix(item.Command, "\n"), "\n", "\r") + "\r"))
		d.quickStatus = "已向「" + target + "」提交命令并回车：" + item.Name
	} else {
		if strings.Contains(item.Command, "\n") {
			d.quickStatus = "多行脚本请复制或运行；直接插入可能执行命令"
			return
		}
		d.term.Paste(item.Command)
		d.quickStatus = "已插入「" + target + "」，请检查后执行：" + item.Name
	}
	if d.workspace == "snippets" {
		d.workspace = "terminal"
		d.pendingTerminalFocus = d.currentSession
	}
}

func (d *desktop) copyQuickCommand(item quickCommand) {
	if err := mygo.Clipboard.Write(transfer.TextData(item.Command)); err != nil {
		d.quickStatus = "复制失败：" + err.Error()
		return
	}
	d.quickStatus = "已复制：" + item.Name
}

func quickCommandPreview(command string) string {
	line, _, multiline := strings.Cut(command, "\n")
	runes := []rune(line)
	if len(runes) > 80 {
		return string(runes[:80]) + "…"
	}
	if multiline {
		return line + " …"
	}
	return line
}

func (d *desktop) quickInsertButton(c *ui.Context, key string, item quickCommand, unavailable bool) {
	if strings.Contains(item.Command, "\n") {
		if ui.Button(c.Key(key), "复制脚本").Disabled(d.quickEditorOpen).Clicked() {
			d.copyQuickCommand(item)
		}
	} else if ui.Button(c.Key(key), "插入").Disabled(unavailable).Clicked() {
		d.sendQuickCommand(item, false)
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
			ui.TextArea(c.Key("quick-text"), &d.quickText).Label("命令或脚本").Placeholder("例如 df -h").Height(90).Grow(2)
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
						ui.Text(c, fmt.Sprintf("%s · %s", item.Name, quickCommandPreview(item.Command))).Grow(1).Wrap()
						d.quickInsertButton(c, "quick-insert-"+item.ID, item, d.term == nil || terminalEnded(d.term))
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
		title := "快速命令"
		if d.workspace == "snippets" {
			title = "命令片段"
		}
		ui.Text(c, title).FontSize(16).Bold()
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
			ui.TextArea(c.Key("quick-text"), &d.quickText).Label("命令或脚本").Placeholder("输入命令或多行脚本，使用空格缩进").Height(120).FillWidth()
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
		if d.workspace == "snippets" {
			d.quickCommandsGrid(c, visible)
			return
		}
		for _, command := range visible {
			item := command
			ui.Column(c).FillWidth().Padding(10).Gap(6).Background(c.Theme().Background).Radius(8).Children(func() {
				ui.Text(c, item.Name).Bold().Wrap()
				ui.Text(c, quickCommandPreview(item.Command)).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					d.quickInsertButton(c, "quick-insert-"+item.ID, item, d.term == nil || terminalEnded(d.term))
					if ui.Button(c.Key("quick-run-"+item.ID), "发送并回车").Disabled(d.term == nil || terminalEnded(d.term)).Clicked() {
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

// The workspace presents snippets as selectable items; actions appear only
// for the selected item, while the terminal sidebar keeps its compact list.
func (d *desktop) quickCommandsGrid(c *ui.Context, commands []quickCommand) {
	ui.Row(c).FillWidth().Wrap().Gap(10).Children(func() {
		for _, command := range commands {
			item := command
			ui.Column(c).Width(300).Padding(10).Gap(6).Background(c.Theme().Surface).Radius(8).Children(func() {
				selected := d.quickSelected == item.ID
				button := ui.Button(c.Key("snippet-select-"+item.ID), "").Label("选择命令片段：" + item.Name).FillWidth().BorderWidth(0).Justify(ui.Start).Disabled(d.quickEditorOpen)
				if selected {
					button = button.Background(c.Theme().SurfacePressed)
				}
				button.Children(func() {
					ui.Column(c).FillWidth().Gap(3).Children(func() {
						ui.Text(c, item.Name).Bold().Wrap()
						ui.Text(c, quickCommandPreview(item.Command)).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
					})
				})
				if button.Clicked() {
					d.quickSelected, d.quickConfirm = item.ID, ""
				}
				if selected {
					ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
						unavailable := d.quickEditorOpen || d.term == nil || terminalEnded(d.term)
						d.quickInsertButton(c, "snippet-insert-"+item.ID, item, unavailable)
						if ui.Button(c.Key("snippet-run-"+item.ID), "发送并回车").Disabled(unavailable).Clicked() {
							d.sendQuickCommand(item, true)
						}
						if ui.Button(c.Key("snippet-edit-"+item.ID), "编辑").Disabled(d.quickEditorOpen).Clicked() {
							d.quickEditorOpen = true
							d.quickEdit, d.quickName, d.quickText, d.quickConfirm = &item, item.Name, item.Command, ""
						}
						label := "删除"
						if d.quickConfirm == item.ID {
							label = "确认删除"
						}
						if ui.Button(c.Key("snippet-delete-"+item.ID), label).Disabled(d.quickEditorOpen).Clicked() {
							d.deleteQuickCommand(item)
						}
					})
				}
			})
		}
	})
}
