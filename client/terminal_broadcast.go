package main

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// The pinned MyGo terminal has no user-input hook. The build wrapper adds
// this interface to a verified, disposable copy of the dependency.
type broadcastTerminal interface {
	NekoSetInputHooks(func(int, int, int, int, int, string, bool), func(string), func(transfer.Data))
	NekoInputAvailable() bool
	NekoReplayKey(int, int, int, int, int, string, bool) bool
	NekoReplayText(string) bool
}

func supportsTerminalBroadcast(term *terminal.Terminal) bool {
	if term == nil {
		return false
	}
	_, ok := any(term).(broadcastTerminal)
	return ok
}

func (d *desktop) bindTerminalBroadcast(s *workspaceSession) {
	if s == nil || s.term == nil {
		return
	}
	source := s.term
	hooks, ok := any(source).(broadcastTerminal)
	if !ok {
		return
	}
	hooks.NekoSetInputHooks(
		func(action, key, mods, consumed, unshifted int, text string, composing bool) {
			targets, group := d.broadcastTargets(s.id, source)
			for _, target := range targets {
				if !any(target.term).(broadcastTerminal).NekoReplayKey(action, key, mods, consumed, unshifted, text, composing) {
					group.broadcast, group.broadcastMembers = false, nil
					d.status = "广播已关闭：部分目标可能已收到输入"
					return
				}
			}
		},
		func(text string) {
			targets, group := d.broadcastTargets(s.id, source)
			for _, target := range targets {
				if !any(target.term).(broadcastTerminal).NekoReplayText(text) {
					group.broadcast, group.broadcastMembers = false, nil
					d.status = "广播已关闭：部分目标可能已收到输入"
					return
				}
			}
		},
		func(data transfer.Data) {
			targets, _ := d.broadcastTargets(s.id, source)
			for _, target := range targets {
				target.term.PasteData(data)
			}
		},
	)
}

// Input hooks run on the UI thread. Validate the selected members before
// sending so a disconnected target cannot silently receive a partial input.
func (d *desktop) broadcastTargets(sourceID int, source *terminal.Terminal) ([]*workspaceSession, *terminalGroup) {
	group := d.terminalGroupFor(sourceID)
	if group == nil || !group.broadcast {
		return nil, nil
	}
	if !group.broadcastMembers[sourceID] {
		return nil, nil
	}
	members := d.terminalGroupSessions(group)
	if len(members) != len(group.members) || len(members) < 2 {
		group.broadcast, group.broadcastMembers = false, nil
		d.status = "广播已关闭：工作区成员已变化"
		return nil, nil
	}
	found := false
	targets := make([]*workspaceSession, 0, len(members)-1)
	for _, member := range members {
		if !group.broadcastMembers[member.id] {
			continue
		}
		if member.term == nil || terminalEnded(member.term) || !supportsTerminalBroadcast(member.term) {
			group.broadcast, group.broadcastMembers = false, nil
			d.status = "广播已关闭：目标终端不可用"
			return nil, nil
		}
		if member.id == sourceID {
			if member.term != source {
				return nil, nil
			}
			found = true
		} else {
			if !any(member.term).(broadcastTerminal).NekoInputAvailable() {
				group.broadcast, group.broadcastMembers = false, nil
				d.status = "广播已关闭：目标终端不可用"
				return nil, nil
			}
			targets = append(targets, member)
		}
	}
	if !found {
		return nil, nil
	}
	return targets, group
}

func (d *desktop) toggleGroupBroadcast(group *terminalGroup) {
	if d.busy || group == nil {
		return
	}
	if group.broadcast {
		group.broadcast, group.broadcastMembers = false, nil
		d.status = "广播输入已关闭"
		return
	}
	members := d.terminalGroupSessions(group)
	if len(members) < 2 || len(members) != len(group.members) {
		d.status = "广播需要至少两个活动终端"
		return
	}
	selected := make(map[int]bool, len(members))
	for _, member := range members {
		if member.term != nil && !terminalEnded(member.term) && supportsTerminalBroadcast(member.term) && any(member.term).(broadcastTerminal).NekoInputAvailable() {
			selected[member.id] = true
		}
	}
	if len(selected) < 2 {
		d.status = "广播不可用：至少需要两个可用终端和广播构建支持"
		return
	}
	group.broadcast, group.broadcastMembers = true, selected
	d.status = fmt.Sprintf("广播输入已开启 · %d 个终端", len(selected))
	d.pendingTerminalFocus = d.currentSession
}

func (d *desktop) toggleBroadcastMember(group *terminalGroup, session *workspaceSession) {
	if d.busy || group == nil || !group.broadcast || session == nil || d.terminalGroupFor(session.id) != group {
		return
	}
	if group.broadcastMembers[session.id] {
		delete(group.broadcastMembers, session.id)
		d.status = "已从广播移出：" + sessionDisplayLabel(session)
	} else {
		if session.term == nil || terminalEnded(session.term) || !supportsTerminalBroadcast(session.term) || !any(session.term).(broadcastTerminal).NekoInputAvailable() {
			d.status = "无法加入广播：终端不可用"
			return
		}
		if group.broadcastMembers == nil {
			group.broadcastMembers = make(map[int]bool)
		}
		group.broadcastMembers[session.id] = true
		d.status = "已加入广播：" + sessionDisplayLabel(session)
	}
	d.pendingTerminalFocus = d.currentSession
}

func (d *desktop) broadcastMemberButton(c *ui.Context, group *terminalGroup, session *workspaceSession) {
	if !group.broadcast {
		return
	}
	selected := group.broadcastMembers[session.id]
	label := fmt.Sprintf("加入广播：%s · 会话 %d", groupSessionDisplayLabel(group, session), session.id)
	if selected {
		label = fmt.Sprintf("移出广播：%s · 会话 %d", groupSessionDisplayLabel(group, session), session.id)
	}
	button := ui.Button(c.Key(fmt.Sprintf("group-broadcast-member-%d", session.id)), "").Label(label).Tooltip(label).Width(30).Height(30).Padding(6).BorderWidth(0).Background(c.Theme().Background).Radius(5).Selected(selected).Disabled(d.busy)
	if selected {
		button = button.Background(c.Theme().SurfacePressed)
	}
	button.Children(func() {
		color := c.Theme().TextMuted
		if selected {
			color = c.Theme().Accent
		}
		ui.Icon(c, broadcastIcon).Width(16).Height(16).TextColor(color)
	})
	if button.Clicked() {
		d.toggleBroadcastMember(group, session)
	}
}
