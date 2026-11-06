package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The left remote pane owns its own path and history, independently of the
// terminal's current session and the right pane's directory.
type secondaryRemotePane struct {
	sessionID                          int
	shell                              *remoteShell
	directory, input, selected, status string
	filter                             string
	showFilter                         bool
	files                              []remoteFile
	history                            fileHistory
	loading                            bool
	generation                         uint64
	paneSelection                      paneSelection
}

type secondaryPaneDrag struct {
	sessionID, rightID        int
	shell, rightShell         *remoteShell
	directory, name, rightDir string
	generation                uint64
	items                     []paneCopyItem
}

func (d *desktop) clearSecondaryConnectIntent() {
	d.secondaryConnectPending = false
	d.secondaryPreviousID, d.secondaryPreviousShell, d.secondaryPreviousTerm = 0, nil, nil
}

func (d *desktop) beginSecondaryConnection() bool {
	if d.busy || len(d.sessions) >= maxWorkspaceSessions {
		return false
	}
	if !d.prepareConnectionNavigation() {
		d.fileStatus = d.status
		return false
	}
	d.secondaryConnectPending = true
	d.secondaryPreviousID, d.secondaryPreviousShell, d.secondaryPreviousTerm = d.currentSession, d.shell, d.term
	d.fileReturnAfterConnect, d.showSecondaryFilePicker = true, false
	return true
}

func (d *desktop) openSecondaryProfile(id profileID) {
	if !d.beginSecondaryConnection() {
		return
	}
	if p, ok := d.currentProfile(id); ok {
		d.openProfileConnection(p)
		return
	}
	d.fileReturnAfterConnect = false
	d.clearSecondaryConnectIntent()
	d.fileStatus = "连接已不存在，请重新选择。"
}

func (d *desktop) openSecondaryCloudAccount(account cloudAccount) {
	if d.beginSecondaryConnection() {
		d.loadCloudAccount(account)
	}
}

func (d *desktop) secondarySession() *workspaceSession {
	for _, s := range d.sessions {
		if s.id == d.secondaryPane.sessionID && s.shell == d.secondaryPane.shell && s.shell != nil && s.term != nil && !terminalEnded(s.term) {
			return s
		}
	}
	return nil
}

func (d *desktop) selectSecondarySession(id int) {
	if d.busy {
		return
	}
	d.showSecondaryFilePicker = false
	if id == 0 {
		d.secondaryPane = secondaryRemotePane{generation: d.secondaryPane.generation + 1}
		return
	}
	for _, s := range d.sessions {
		if s.id != id || s.shell == nil || s.term == nil || terminalEnded(s.term) {
			continue
		}
		if d.secondaryPane.sessionID == id && d.secondaryPane.shell == s.shell {
			return
		}
		dir := s.fileDir
		if id == d.currentSession {
			dir = d.fileDir
		}
		if strings.TrimSpace(dir) == "" {
			dir = "."
		}
		d.secondaryPane = secondaryRemotePane{sessionID: id, shell: s.shell, directory: dir, input: dir, generation: d.secondaryPane.generation + 1}
		d.requestSecondaryPane(dir, -1)
		return
	}
}

func (d *desktop) secondaryFilePicker(c *ui.Context) {
	label := "本机"
	if d.secondaryPane.sessionID != 0 {
		label = "会话已断开"
		if s := d.secondarySession(); s != nil {
			label = s.label
		}
	}
	button := ui.Button(c.Key("secondary-picker"), label+" ▾").Label("选择左侧文件主机").Tooltip("选择左侧文件主机").Disabled(d.busy)
	if button.Clicked() {
		d.showSecondaryFilePicker = !d.showSecondaryFilePicker
		if d.showSecondaryFilePicker {
			d.secondaryFileSearch = ""
		}
	}
	ui.PopoverBase(c, button, &d.showSecondaryFilePicker, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(300).Padding(10).Gap(6).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
				ui.TextInput(c.Key("secondary-search"), &d.secondaryFileSearch).Label("搜索左侧文件主机").Placeholder("搜索主机或会话").FillWidth()
				query := strings.ToLower(strings.TrimSpace(d.secondaryFileSearch))
				matches := func(s string) bool { return query == "" || strings.Contains(strings.ToLower(s), query) }
				ui.Scroll(c.Key("secondary-picker-list")).Label("左侧文件主机列表").Height(240).FillWidth().Children(func() {
					shown := 0
					if matches("本机 local") {
						shown++
						if ui.Button(c.Key("secondary-local"), "本机").Label("左侧使用本机文件").FillWidth().Justify(ui.Start).Clicked() {
							d.selectSecondarySession(0)
						}
					}
					for _, session := range d.sessions {
						s := session
						if s.shell == nil || s.term == nil || terminalEnded(s.term) || !matches(s.label) {
							continue
						}
						shown++
						label := fmt.Sprintf("%s · 会话 %d", s.label, s.id)
						if ui.Button(c.Key(fmt.Sprintf("secondary-session-%d", s.id)), label).Label("左侧文件会话：" + label).FillWidth().Justify(ui.Start).Clicked() {
							d.selectSecondarySession(s.id)
						}
					}
					blocked := d.connectionNavigationBlocked() || len(d.sessions) >= maxWorkspaceSessions
					for _, profile := range d.profiles {
						p := profile
						name := p.Label
						if name == "" {
							name = p.Settings.User + "@" + p.Settings.Host
						}
						if !matches(name + " " + p.Settings.Host + " " + p.Settings.User + " " + p.Group) {
							continue
						}
						shown++
						if ui.Button(c.Key("secondary-profile-"+string(p.ID)), name+" · 本机").Label("左侧主机：" + name + " · 本机").FillWidth().Justify(ui.Start).Disabled(blocked).Clicked() {
							d.openSecondaryProfile(p.ID)
						}
					}
					if d.cloud != nil {
						for _, entry := range d.accounts {
							a := entry
							name := a.Label
							if name == "" {
								name = a.Username + "@" + a.Host
							}
							if !matches(name + " " + a.Host + " " + a.Username) {
								continue
							}
							shown++
							if ui.Button(c.Key("secondary-cloud-"+a.ID), name+" · 云端").Label("左侧主机：" + name + " · 云端").FillWidth().Justify(ui.Start).Disabled(blocked).Clicked() {
								d.openSecondaryCloudAccount(a)
							}
						}
					}
					if shown == 0 {
						ui.Text(c, "没有匹配的主机").FontSize(12).TextColor(c.Theme().TextMuted)
					}
				})
			})
		})
	})
}

func (d *desktop) requestSecondaryPane(dir string, historyIndex int) {
	if d.win == nil || d.secondarySession() == nil || d.busy {
		return
	}
	dir = strings.TrimSpace(dir)
	if dir == "" || strings.ContainsAny(dir, "\x00\r\n") {
		d.secondaryPane.status = "请输入有效目录。"
		return
	}
	p := &d.secondaryPane
	p.input, p.loading, p.status = dir, true, "正在读取目录…"
	p.generation++
	generation, id, shell := p.generation, p.sessionID, p.shell
	go func() {
		files, err := listRemote(shell, dir)
		d.win.Update(func() { d.finishSecondaryPaneLoad(generation, id, shell, dir, historyIndex, files, err) })
	}()
}

func (d *desktop) finishSecondaryPaneLoad(generation uint64, id int, shell *remoteShell, dir string, historyIndex int, files []remoteFile, err error) {
	p := &d.secondaryPane
	if p.generation != generation || p.sessionID != id || p.shell != shell || d.secondarySession() == nil {
		return
	}
	p.loading = false
	if err != nil {
		p.input = p.directory
		p.status = "读取目录失败：" + err.Error()
		return
	}
	if historyIndex < 0 || !p.history.commitNavigation(historyIndex, dir) {
		p.history.visit(dir)
	}
	p.directory, p.input, p.files, p.selected = dir, dir, files, ""
	p.paneSelection = paneSelection{}
	p.status = fmt.Sprintf("%d 个条目", len(files))
}

func (d *desktop) secondaryFilePaneView(c *ui.Context, listHeight float32) {
	p := &d.secondaryPane
	ui.Row(c).FillWidth().Height(36).AlignItems(ui.Center).Children(func() { d.secondaryFilePicker(c); d.leftPaneToolbar(c) })
	if d.secondarySession() == nil {
		ui.Text(c, "会话已断开，请选择其他主机。").Wrap()
		return
	}
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		backIndex, backDir, back := p.history.target(-1)
		if ui.Button(c.Key("secondary-back"), "‹").Label("左侧目录后退").Tooltip("后退").Width(36).Disabled(d.busy || p.loading || !back).Clicked() {
			d.requestSecondaryPane(backDir, backIndex)
		}
		forwardIndex, forwardDir, forward := p.history.target(1)
		if ui.Button(c.Key("secondary-forward"), "›").Label("左侧目录前进").Tooltip("前进").Width(36).Disabled(d.busy || p.loading || !forward).Clicked() {
			d.requestSecondaryPane(forwardDir, forwardIndex)
		}
		if ui.Button(c.Key("secondary-parent"), "↑").Label("左侧上一级目录").Tooltip("上一级").Width(36).Disabled(d.busy || p.loading).Clicked() {
			d.requestSecondaryPane(path.Dir(p.directory), -1)
		}
		ui.TextInput(c.Key("secondary-path"), &p.input).Label("左侧远程目录").Placeholder("远程目录路径").OnChange(func() {
			p.selected = ""
			p.paneSelection = paneSelection{}
			if p.loading {
				p.generation++
				p.loading = false
			}
			if p.input != p.directory {
				p.status = "打开输入的目录后才能传输。"
			}
		}).OnSubmit(func() { d.requestSecondaryPane(p.input, -1) }).Grow(1)
		if ui.Button(c.Key("secondary-refresh"), "↻").Label("打开左侧目录").Tooltip("打开或刷新目录").Width(36).Disabled(d.busy).Clicked() {
			d.requestSecondaryPane(p.input, -1)
		}
	})
	if p.showFilter {
		ui.TextInput(c.Key("secondary-pane-filter"), &p.filter).Label("筛选左侧远程文件").Placeholder("筛选文件").OnChange(func() {
			p.selected = ""
			p.paneSelection = paneSelection{}
		})
	}
	d.leftPaneActionView(c)
	if p.status != "" {
		ui.Text(c, p.status).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	ui.Row(c).FillWidth().Padding(6, 8).Children(func() {
		ui.Text(c, "名称").Grow(1).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, "大小").Width(70).FontSize(12).TextColor(c.Theme().TextMuted)
	})
	visible := displayedFiles(p.files, p.filter, "name", false, false)
	ui.Scroll(c.Key("secondary-list")).Label("左侧远程文件列表").Height(listHeight).FillWidth().Children(func() {
		for _, entry := range visible {
			f := entry
			label := f.Name
			if f.IsDir {
				label = "📁 " + label
			}
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				row := ui.Button(c.Key("secondary-file-"+f.Name), "").Label(label).BorderWidth(0).Justify(ui.Start).Grow(1).Gap(8).Disabled(d.busy || p.loading || p.input != p.directory)
				if p.selected == f.Name || p.paneSelection.names[f.Name] {
					row = row.Selected(true).Background(c.Theme().SurfacePressed)
				}
				row.Children(func() {
					icon, color := documentIcon, c.Theme().TextMuted
					if f.IsDir {
						icon, color = fileIcon, c.Theme().AccentHover
					}
					ui.Icon(c, icon).Width(16).Height(16).Shrink(0).TextColor(color)
					ui.Text(c, f.Name)
				})
				item := d.leftPaneDragForFile(f)
				if eligiblePaneItem(f) {
					row = row.Drag(item)
				}
				row.ContextMenu(func(menu *ui.Menu) {
					d.leftPaneContextMenu(menu, f.Name)
					if f.IsDir && menu.Item("打开目录").Chosen() {
						d.requestSecondaryPane(path.Join(p.directory, f.Name), -1)
					}
					if eligiblePaneItem(f) && menu.Item("复制到右侧当前目录").Disabled(!d.canCopySecondaryToRight(item)).Chosen() {
						d.copySecondaryToRight(item)
					}
				})
				if row.DoubleClicked() && f.IsDir {
					d.requestSecondaryPane(path.Join(p.directory, f.Name), -1)
				} else if row.Clicked() {
					selected, err := p.paneSelection.choose(f.Name, visible, row.ClickModifiers())
					if err != nil {
						p.status = err.Error()
					} else {
						p.selected = selected
					}
				}
				ui.Text(c, fileSizeLabel(f)).Width(70).FontSize(12).TextColor(c.Theme().TextMuted)
			})
		}
	})
	if len(p.paneSelection.names) > 1 {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("已选择 %d 项", len(p.paneSelection.names))).FontSize(12).TextColor(c.Theme().TextMuted)
			if ui.Button(c.Key("secondary-clear-selection"), "清除").Label("清除左侧文件选择").Disabled(d.busy).Clicked() {
				p.paneSelection = paneSelection{}
				p.selected = ""
			}
		})
	}
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		left, right := d.leftPaneDrag(p.selected), d.rightPaneDrag(d.selectedFile)
		leftLabel, rightLabel := "复制到右侧 →", "← 复制到左侧"
		if len(left.items) > 1 {
			leftLabel = fmt.Sprintf("复制 %d 项 →", len(left.items))
		}
		if len(right.items) > 1 {
			rightLabel = fmt.Sprintf("← 复制 %d 项", len(right.items))
		}
		if ui.Button(c.Key("secondary-copy-right"), leftLabel).Disabled(!d.canCopySecondaryToRight(left)).Clicked() {
			d.copySecondaryToRight(left)
		}
		if ui.Button(c.Key("secondary-copy-left"), rightLabel).Disabled(!d.canCopyRightToSecondary(right)).Clicked() {
			d.copyRightToSecondary(right)
		}
	})
	if d.cancelTransfer != nil && ui.Button(c.Key("left-cancel-transfer"), "取消传输").Clicked() {
		d.cancelFileTransfer()
	}
}

func (d *desktop) leftPaneDrag(name string) secondaryPaneDrag {
	p := d.secondaryPane
	return secondaryPaneDrag{sessionID: p.sessionID, shell: p.shell, directory: p.directory, generation: p.generation, name: name, rightID: d.currentSession, rightShell: d.shell, rightDir: d.fileDir, items: p.paneSelection.itemsFor(name, p.files)}
}
func (d *desktop) leftPaneDragForFile(file remoteFile) secondaryPaneDrag {
	p := d.secondaryPane
	return secondaryPaneDrag{sessionID: p.sessionID, shell: p.shell, directory: p.directory, generation: p.generation, name: file.Name, rightID: d.currentSession, rightShell: d.shell, rightDir: d.fileDir, items: p.paneSelection.itemsForFile(file)}
}
func (d *desktop) rightPaneDrag(name string) remotePaneDrag {
	p := d.secondaryPane
	return remotePaneDrag{sessionID: d.currentSession, shell: d.shell, directory: d.fileDir, name: name, secondaryID: p.sessionID, secondaryShell: p.shell, secondaryDir: p.directory, secondaryGeneration: p.generation, localDirectory: d.localPane.directory, localGeneration: d.localPane.generation, items: d.filePaneSelection.itemsFor(name, d.files)}
}
func (d *desktop) rightPaneDragForFile(file remoteFile) remotePaneDrag {
	p := d.secondaryPane
	return remotePaneDrag{sessionID: d.currentSession, shell: d.shell, directory: d.fileDir, name: file.Name, secondaryID: p.sessionID, secondaryShell: p.shell, secondaryDir: p.directory, secondaryGeneration: p.generation, localDirectory: d.localPane.directory, localGeneration: d.localPane.generation, items: d.filePaneSelection.itemsForFile(file)}
}
func (d *desktop) remotePanePairValid(left secondaryPaneDrag) bool {
	p := d.secondaryPane
	h := p.history
	if len(h.paths) == 0 || h.index < 0 || h.index >= len(h.paths) || h.paths[h.index] != p.directory {
		return false
	}
	return !d.busy && d.win != nil && d.secondarySession() != nil && !p.loading && p.input == p.directory && p.directory != "" && left.sessionID == p.sessionID && left.shell == p.shell && left.directory == p.directory && left.generation == p.generation && left.rightID == d.currentSession && left.rightShell == d.shell && left.rightDir == d.fileDir && !d.fileSessionUnavailable() && strings.TrimSpace(d.fileDir) != ""
}
func (d *desktop) canCopySecondaryToRight(item secondaryPaneDrag) bool {
	if !d.remotePanePairValid(item) || !validDirectoryName(item.name) {
		return false
	}
	if !d.rightDirectoryReady() {
		return false
	}
	if !paneItemsInclude(item.items, item.name) || !paneItemsMatch(item.items, d.secondaryPane.files) {
		return false
	}
	for _, f := range d.secondaryPane.files {
		if f.Name == item.name && eligiblePaneItem(f) {
			return true
		}
	}
	return false
}

func (d *desktop) rightDirectoryReady() bool {
	h := d.fileHistory
	return len(h.paths) > 0 && h.index >= 0 && h.index < len(h.paths) && h.paths[h.index] == d.fileDir
}
func (d *desktop) canCopyRightToSecondary(item remotePaneDrag) bool {
	left := secondaryPaneDrag{sessionID: item.secondaryID, shell: item.secondaryShell, directory: item.secondaryDir, generation: item.secondaryGeneration, rightID: item.sessionID, rightShell: item.shell, rightDir: item.directory}
	if !d.remotePanePairValid(left) || !validDirectoryName(item.name) {
		return false
	}
	if !paneItemsInclude(item.items, item.name) || !paneItemsMatch(item.items, d.files) {
		return false
	}
	for _, f := range d.files {
		if f.Name == item.name && eligiblePaneItem(f) {
			return true
		}
	}
	return false
}
func (d *desktop) copySecondaryToRight(item secondaryPaneDrag) {
	if !d.canCopySecondaryToRight(item) {
		d.fileStatus = "会话或目录已改变，请重新选择文件"
		return
	}
	d.startPaneCopy(item.shell, item.directory, item.rightShell, item.rightDir, item.items, true)
}
func (d *desktop) copyRightToSecondary(item remotePaneDrag) {
	if !d.canCopyRightToSecondary(item) {
		d.fileStatus = "会话或目录已改变，请重新选择文件"
		return
	}
	d.startPaneCopy(item.shell, item.directory, item.secondaryShell, item.secondaryDir, item.items, false)
}
func (d *desktop) startPaneCopy(src *remoteShell, srcDir string, dst *remoteShell, dstDir string, items []paneCopyItem, targetRight bool) {
	if len(items) == 0 || len(items) > maxBatchFiles {
		d.fileStatus = "请选择有效的文件或目录"
		return
	}
	rightID, rightShell, rightDir := d.currentSession, d.shell, d.fileDir
	leftID, leftShell, leftDir, leftGeneration := d.secondaryPane.sessionID, d.secondaryPane.shell, d.secondaryPane.directory, d.secondaryPane.generation
	localDir, localGeneration := d.localPane.directory, d.localPane.generation
	items = append([]paneCopyItem(nil), items...)
	d.setBatchFailures(rightID, rightShell, nil)
	d.busy, d.fileStatus = true, "正在传输…"
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelTransfer, d.transferSessionID = cancel, rightID
	go func() {
		defer cancel()
		var partial bool
		var err error
		var batch paneBatchResult
		if len(items) == 1 {
			partial, err = copyPaneExpectedEntryContext(ctx, src, srcDir, dst, dstDir, items[0].name, &items[0].directory, func(read int64) {
				d.win.Update(func() {
					if ctx.Err() == nil && d.cancelTransfer != nil && d.transferSessionID == rightID {
						d.transferProgress(rightID, rightShell, rightDir, read)
					}
				})
			})
		} else {
			batch = runPaneBatch(ctx, items, func(item paneCopyItem, report func(int64)) (bool, error) {
				return copyPaneExpectedEntryContext(ctx, src, srcDir, dst, dstDir, item.name, &item.directory, report)
			}, func(index, total int, name string, read int64) {
				d.win.Update(func() {
					if ctx.Err() != nil || d.cancelTransfer == nil || d.transferSessionID != rightID {
						return
					}
					status := fmt.Sprintf("正在传输 %d/%d · %s · 已读取 %s", index, total, name, fileSizeLabel(remoteFile{Size: read}))
					if d.currentSession == rightID && d.shell == rightShell && d.fileDir == rightDir {
						d.fileStatus = status
						return
					}
					for _, session := range d.sessions {
						if session.id == rightID && session.shell == rightShell && session.fileDir == rightDir {
							session.fileStatus = status
							break
						}
					}
				})
			})
		}
		d.win.Update(func() {
			changed := err == nil || partial
			if len(items) > 1 {
				changed = batch.succeeded > 0 || batch.partial > 0
				d.setBatchFailures(rightID, rightShell, batch.errors)
				d.finishTransferStatus(rightID, rightShell, rightDir, batch.status(), targetRight && changed)
			} else if partial && err != nil {
				status := "目录复制失败，已创建的内容保留在目标目录：" + transferErrorText(err)
				if errors.Is(err, context.Canceled) {
					status = "目录复制已取消，已创建的内容保留在目标目录"
				}
				d.finishTransferStatus(rightID, rightShell, rightDir, status, targetRight)
			} else {
				d.finishTransfer(rightID, rightShell, rightDir, targetRight, err)
			}
			p := &d.secondaryPane
			if changed && !targetRight {
				if leftID != 0 && p.sessionID == leftID && p.shell == leftShell && p.directory == leftDir && p.input == leftDir && p.generation == leftGeneration {
					d.requestSecondaryPane(leftDir, -1)
				} else if leftID == 0 && p.sessionID == 0 && d.localPane.directory == localDir && d.localPane.generation == localGeneration && sameLocalDirectory(d.localPane.input, localDir) {
					d.requestLocalPane(localDir, -1)
				}
			}
		})
	}()
}
