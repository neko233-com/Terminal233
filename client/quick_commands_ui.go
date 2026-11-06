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
	saved, err := readQuickCommandFile(d.quickPath)
	if err != nil {
		d.quickStatus = "读取失败，原文件未覆盖：" + err.Error()
		return
	}
	d.quickCommands, d.quickPackages, d.quickEdit, d.quickConfirm = saved.Items, saved.Packages, nil, ""
	d.quickPackageConfirm, d.quickPackageName = false, ""
	if d.quickPackageFilter != "" && d.quickPackageFilter != "-" && quickPackageIndex(saved.Packages, d.quickPackageFilter) < 0 {
		d.quickPackageFilter = ""
	}
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
	item.PackageID = d.quickPackageID
	saved, err := updateQuickCommandFile(d.quickPath, func(saved *quickCommandFile) error {
		if item.PackageID != "" && quickPackageIndex(saved.Packages, item.PackageID) < 0 {
			return errors.New("分组已被删除，请刷新后重试")
		}
		items := saved.Items
		if expected == nil {
			if len(items) >= maxQuickCommands {
				return errors.New("快速命令已达到 100 条")
			}
			saved.Items = append(items, item)
			return nil
		}
		for i, current := range items {
			if current.ID == expected.ID {
				if current != *expected {
					return errors.New("命令已在其他窗口修改，请刷新后重试")
				}
				item.ID = current.ID
				items[i] = item
				saved.Items = items
				return nil
			}
		}
		return errors.New("命令已被删除，请刷新后重试")
	})
	if err != nil {
		d.quickStatus = "保存失败：" + err.Error()
		return false
	}
	d.quickCommands, d.quickPackages, d.quickEdit, d.quickConfirm = saved.Items, saved.Packages, nil, ""
	d.quickName, d.quickText = "", ""
	d.quickStatus = "快速命令已保存到本机"
	d.quickSelected = item.ID
	return true
}

func (d *desktop) deleteQuickCommand(expected quickCommand) {
	if d.quickConfirm != expected.ID {
		d.quickConfirm = expected.ID
		d.quickStatus = "再次点击确认删除：" + expected.Name
		return
	}
	saved, err := updateQuickCommandFile(d.quickPath, func(saved *quickCommandFile) error {
		items := saved.Items
		for i, current := range items {
			if current.ID == expected.ID {
				if current != expected {
					return errors.New("命令已在其他窗口修改，请刷新后重试")
				}
				saved.Items = append(items[:i], items[i+1:]...)
				return nil
			}
		}
		return errors.New("命令已被删除，请刷新后重试")
	})
	if err != nil {
		d.quickStatus, d.quickConfirm = "删除失败："+err.Error(), ""
		return
	}
	d.quickCommands, d.quickPackages, d.quickEdit, d.quickConfirm = saved.Items, saved.Packages, nil, ""
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
				d.quickPackageManaging, d.quickPackageConfirm = false, false
				d.quickPackageID = d.quickPackageFilter
				if d.quickPackageID == "-" {
					d.quickPackageID = ""
				}
			}
			if ui.Button(c.Key("quick-packages"), "分组").Disabled(d.quickEditorOpen).Clicked() {
				d.quickPackageManaging = !d.quickPackageManaging
				d.quickPackageConfirm, d.quickPackageName = false, ""
			}
			if ui.Button(c.Key("quick-refresh"), "刷新").Disabled(d.quickEditorOpen).Clicked() {
				d.reloadQuickCommands()
			}
		})
		if len(d.quickPackages) > 0 && !d.quickEditorOpen {
			label := quickPackageLabel(d.quickPackages, d.quickPackageFilter, true)
			if ui.Select(c.Key("quick-package-filter"), &label, quickPackageLabels(d.quickPackages, true)).Label("筛选命令分组").FillWidth().Changed() {
				d.quickPackageFilter = quickPackageForLabel(d.quickPackages, label, true)
				d.quickPackageName, d.quickPackageConfirm = "", false
			}
		}
		if d.quickPackageManaging && !d.quickEditorOpen {
			ui.TextInput(c.Key("quick-package-name"), &d.quickPackageName).Label("分组名称").Placeholder("分组名称").FillWidth()
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				if ui.Button(c.Key("quick-package-add"), "新建分组").Clicked() {
					d.saveQuickPackage(false)
				}
				if ui.Button(c.Key("quick-package-rename"), "重命名分组").Disabled(quickPackageIndex(d.quickPackages, d.quickPackageFilter) < 0).Clicked() {
					d.saveQuickPackage(true)
				}
				label := "移除分组"
				if d.quickPackageConfirm {
					label = "确认移除分组"
				}
				if ui.Button(c.Key("quick-package-delete"), label).Disabled(quickPackageIndex(d.quickPackages, d.quickPackageFilter) < 0).Clicked() {
					d.deleteQuickPackage()
				}
			})
		}
		if d.quickEditorOpen {
			ui.Text(c, "命令保存在本机明文文件中，请勿填写密码或令牌。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			ui.TextInput(c.Key("quick-name"), &d.quickName).Label("命令名称").Placeholder("命令名称").FillWidth()
			ui.TextArea(c.Key("quick-text"), &d.quickText).Label("命令或脚本").Placeholder("输入命令或多行脚本，使用空格缩进").Height(120).FillWidth()
			if len(d.quickPackages) > 0 {
				label := quickPackageLabel(d.quickPackages, d.quickPackageID, false)
				if ui.Select(c.Key("quick-package-edit"), &label, quickPackageLabels(d.quickPackages, false)).Label("片段分组").FillWidth().Changed() {
					d.quickPackageID = quickPackageForLabel(d.quickPackages, label, false)
				}
			}
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
		visible := filterQuickCommands(filterQuickPackage(d.quickCommands, d.quickPackageFilter), d.quickFilter)
		if len(visible) == 0 {
			ui.Text(c, "没有匹配的快速命令").FontSize(12).TextColor(c.Theme().TextMuted)
		}
		if d.workspace == "snippets" {
			d.quickCommandsGrid(c, visible)
			return
		}
		for _, command := range visible {
			item := command
			selected := d.quickSelected == item.ID
			ui.Column(c).FillWidth().Padding(6).Gap(4).Background(c.Theme().Background).Radius(8).Children(func() {
				row := ui.Button(c.Key("quick-select-"+item.ID), "").Label("选择快速命令：" + item.Name).FillWidth().BorderWidth(0).Justify(ui.Start).Disabled(d.quickEditorOpen)
				if selected {
					row = row.Background(c.Theme().SurfacePressed)
				}
				row.Children(func() {
					ui.Column(c).FillWidth().Gap(3).Children(func() {
						ui.Text(c, item.Name).Bold().Wrap()
						ui.Text(c, quickCommandPreview(item.Command)).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
					})
				})
				row.ContextMenu(func(menu *ui.Menu) {
					if menu.Item("编辑").Disabled(d.quickEditorOpen).Chosen() {
						d.quickSelected, d.quickEditorOpen = item.ID, true
						d.quickEdit, d.quickName, d.quickText, d.quickConfirm = &item, item.Name, item.Command, ""
						d.quickPackageID = item.PackageID
					}
					if menu.Item("删除…").Disabled(d.quickEditorOpen).Chosen() {
						d.quickSelected = item.ID
						d.deleteQuickCommand(item)
					}
				})
				if row.DoubleClicked() && !d.quickEditorOpen && d.term != nil && !terminalEnded(d.term) {
					d.quickSelected, d.quickConfirm = item.ID, ""
					d.sendQuickCommand(item, true)
				} else if row.Clicked() && !d.quickEditorOpen {
					d.quickSelected, d.quickConfirm = item.ID, ""
				}
				if selected && !d.quickEditorOpen {
					ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
						d.quickInsertButton(c, "quick-insert-"+item.ID, item, d.term == nil || terminalEnded(d.term))
						if ui.Button(c.Key("quick-run-"+item.ID), "运行").Disabled(d.term == nil || terminalEnded(d.term)).Clicked() {
							d.sendQuickCommand(item, true)
						}
						if d.quickConfirm == item.ID && ui.Button(c.Key("quick-delete-"+item.ID), "确认删除").Clicked() {
							d.deleteQuickCommand(item)
						}
					})
				}
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
				if button.DoubleClicked() && !d.quickEditorOpen {
					d.quickSelected, d.quickEditorOpen = item.ID, true
					d.quickEdit, d.quickName, d.quickText, d.quickConfirm = &item, item.Name, item.Command, ""
					d.quickPackageID = item.PackageID
				} else if button.Clicked() {
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
							d.quickPackageID = item.PackageID
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
