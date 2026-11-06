package main

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
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
					group.broadcast = false
					d.status = "广播已关闭：部分目标可能已收到输入"
					return
				}
			}
		},
		func(text string) {
			targets, group := d.broadcastTargets(s.id, source)
			for _, target := range targets {
				if !any(target.term).(broadcastTerminal).NekoReplayText(text) {
					group.broadcast = false
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

// Input hooks run on the UI thread. Validate the whole membership before
// sending so a disconnected target cannot silently receive a partial input.
func (d *desktop) broadcastTargets(sourceID int, source *terminal.Terminal) ([]*workspaceSession, *terminalGroup) {
	group := d.terminalGroupFor(sourceID)
	if group == nil || !group.broadcast {
		return nil, nil
	}
	members := d.terminalGroupSessions(group)
	if len(members) != len(group.members) || len(members) < 2 {
		group.broadcast = false
		d.status = "广播已关闭：工作区成员已变化"
		return nil, nil
	}
	found := false
	targets := make([]*workspaceSession, 0, len(members)-1)
	for _, member := range members {
		if member.term == nil || terminalEnded(member.term) || !supportsTerminalBroadcast(member.term) {
			group.broadcast = false
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
				group.broadcast = false
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
		group.broadcast = false
		d.status = "广播输入已关闭"
		return
	}
	members := d.terminalGroupSessions(group)
	if len(members) < 2 || len(members) != len(group.members) {
		d.status = "广播需要至少两个活动终端"
		return
	}
	for _, member := range members {
		if member.term == nil || terminalEnded(member.term) || !supportsTerminalBroadcast(member.term) || !any(member.term).(broadcastTerminal).NekoInputAvailable() {
			d.status = "广播不可用：终端已断开或构建缺少输入支持"
			return
		}
	}
	group.broadcast = true
	d.status = fmt.Sprintf("广播输入已开启 · %d 个终端", len(members))
	d.pendingTerminalFocus = d.currentSession
}
