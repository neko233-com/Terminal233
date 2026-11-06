package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// Drafts contain only live session references and never persist credentials.
type quickMultiDraft struct {
	item     quickCommand
	selected map[int]*terminal.Terminal
}

func (d *desktop) liveQuickTargets() int {
	count := 0
	for _, s := range d.sessions {
		if s.term != nil && !terminalEnded(s.term) {
			count++
		}
	}
	return count
}

func (d *desktop) beginQuickMulti(item quickCommand) {
	if d.busy || d.quickEditorOpen || d.liveQuickTargets() < 2 || validateQuickCommand(item.Name, item.Command) != nil {
		return
	}
	d.quickMulti = &quickMultiDraft{item: item, selected: make(map[int]*terminal.Terminal)}
	d.quickStatus = ""
}

func (d *desktop) quickMultiTargets() ([]*workspaceSession, error) {
	draft := d.quickMulti
	if d.busy || d.quickEditorOpen || draft == nil || len(draft.selected) == 0 || validateQuickCommand(draft.item.Name, draft.item.Command) != nil {
		return nil, errors.New("请选择有效的目标终端")
	}
	items := d.quickCommands
	if d.quickPath != "" {
		file, err := readQuickCommandFile(d.quickPath)
		if err != nil {
			return nil, errors.New("无法读取片段，请刷新后重试")
		}
		items = file.Items
	}
	valid := false
	for _, current := range items {
		if current.ID == draft.item.ID && current == draft.item {
			valid = true
			break
		}
	}
	if !valid {
		return nil, errors.New("片段已修改或删除，请刷新后重新选择")
	}
	var targets []*workspaceSession
	for _, s := range d.sessions {
		if term, ok := draft.selected[s.id]; ok {
			if term == nil || s.term != term || terminalEnded(term) {
				return nil, errors.New("目标终端已断开或改变，请重新选择")
			}
			targets = append(targets, s)
		}
	}
	if len(targets) != len(draft.selected) {
		return nil, errors.New("目标终端已关闭，请重新选择")
	}
	return targets, nil
}

func (d *desktop) executeQuickMulti() {
	targets, err := d.quickMultiTargets()
	if err != nil {
		d.quickStatus = err.Error()
		return
	}
	item := d.quickMulti.item
	command := []byte(strings.ReplaceAll(strings.TrimSuffix(item.Command, "\n"), "\n", "\r") + "\r")
	for _, target := range targets {
		target.term.Send(command)
	}
	d.quickMulti = nil
	d.quickStatus = fmt.Sprintf("已向 %d 个终端提交并回车：%s", len(targets), item.Name)
	d.status = d.quickStatus
	if d.workspace == "snippets" {
		d.workspace, d.pendingTerminalFocus = "terminal", d.currentSession
	}
}

func (d *desktop) quickMultiButton(c *ui.Context, prefix string, item quickCommand) {
	active := d.quickMulti != nil && d.quickMulti.item.ID == item.ID
	if ui.Button(c.Key(prefix+item.ID), "多终端运行").Selected(active).Disabled(d.busy || d.quickEditorOpen || d.liveQuickTargets() < 2).Clicked() {
		if d.quickMulti != nil && d.quickMulti.item.ID == item.ID {
			d.quickMulti = nil
		} else {
			d.beginQuickMulti(item)
		}
	}
}

func (d *desktop) quickMultiPanel(c *ui.Context, item quickCommand) {
	draft := d.quickMulti
	if draft == nil || draft.item.ID != item.ID {
		return
	}
	ui.Column(c).FillWidth().Padding(8).Gap(6).Background(c.Theme().SurfaceHover).Radius(6).Children(func() {
		ui.Text(c, fmt.Sprintf("选择目标 · %d", len(draft.selected))).FontSize(12).Bold()
		ui.Scroll(c.Key("quick-multi-targets-" + item.ID)).Label("选择片段的目标终端").FillWidth().Height(min(float32(150), float32(len(d.sessions)*28))).Children(func() {
			for _, entry := range d.sessions {
				s := entry
				_, selected := draft.selected[s.id]
				label := fmt.Sprintf("选择目标：%s · 会话 %d", sessionDisplayLabel(s), s.id)
				visible := fmt.Sprintf("%s · %d", sessionDisplayLabel(s), s.id)
				if ui.Checkbox(c.Key(fmt.Sprintf("quick-multi-target-%s-%d", item.ID, s.id)), &selected, visible).Label(label).Disabled(d.busy || (!selected && (s.term == nil || terminalEnded(s.term)))).Changed() {
					if selected {
						draft.selected[s.id] = s.term
					} else {
						delete(draft.selected, s.id)
					}
					d.quickStatus = ""
				}
			}
		})
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			if ui.PrimaryButton(c.Key("quick-multi-send-"+item.ID), "发送并回车").Label("向所选终端发送并回车：" + item.Name).Disabled(d.busy || len(draft.selected) == 0).Clicked() {
				d.executeQuickMulti()
			}
			if ui.Button(c.Key("quick-multi-cancel-"+item.ID), "取消").Clicked() {
				d.quickMulti = nil
			}
		})
	})
}
