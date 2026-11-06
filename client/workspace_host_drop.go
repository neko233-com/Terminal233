package main

import "strconv"

// A drop carries identity and endpoint, but never credentials or host trust.
// The target pointers prevent a delayed connection from joining a different
// workspace after the original target has been closed or rearranged.
type workspaceHostDrop struct {
	target                                  *workspaceSession
	group                                   *terminalGroup
	profile                                 profileID
	cloudID, cloudURL, cloudUser, cloudTeam string
	host, port, user                        string
}

func (p *workspaceHostDrop) targetStillValid(d *desktop) bool {
	if p == nil || p.target == nil || p.target.term == nil || terminalEnded(p.target.term) {
		return false
	}
	for _, s := range d.sessions {
		if s == p.target {
			g := d.terminalGroupFor(s.id)
			return g == p.group && (g == nil || len(g.members) < maxWorkspaceSessions)
		}
	}
	return false
}

func (p *workspaceHostDrop) matchesForm(d *desktop, settings connectionSettings, fileOnly bool) bool {
	if p == nil || fileOnly || d.workspace != "connections" || settings.Host != p.host || settings.Port != p.port || settings.User != p.user {
		return false
	}
	if p.profile != "" {
		return !d.settingsFromCloud && d.selectedProfile == string(p.profile)
	}
	return d.settingsFromCloud && p.cloudID != "" && d.selectedCloudProfile == p.cloudID && d.cloud != nil && d.cloud.base != nil && d.cloud.token != "" && d.cloud.base.String() == p.cloudURL && d.cloudUser == p.cloudUser && d.cloud.teamID == p.cloudTeam
}

func (d *desktop) startWorkspaceHostDrop(targetID int, local profileID, cloud cloudGroupDragID) {
	if d.busy || d.workspace != "connections" {
		return
	}
	if d.terminalLimitReached() {
		d.status = "终端数量已达上限，请关闭终端后重试"
		return
	}
	var target *workspaceSession
	for _, s := range d.sessions {
		if s.id == targetID {
			target = s
			break
		}
	}
	intent := &workspaceHostDrop{target: target, group: d.terminalGroupFor(targetID)}
	if !intent.targetStillValid(d) {
		d.status = "目标终端已关闭或工作区已满"
		return
	}
	if !d.prepareConnectionNavigation() {
		return
	}
	// A new drop replaces any previous pending host connection.
	d.pendingWorkspaceHost = nil
	// A host dropped on a terminal creates an SSH terminal, never an SFTP-only connection.
	d.fileReturnAfterConnect = false
	d.clearSecondaryConnectIntent()
	if local != "" {
		p, ok := d.currentProfile(local)
		if !ok {
			d.status = "主机已不存在，请刷新连接列表"
			return
		}
		d.loadProfile(p)
		intent.profile, intent.host, intent.port, intent.user = p.ID, d.settings.Host, d.settings.Port, d.settings.User
		d.pendingWorkspaceHost = intent
		if !d.profileSecretError && d.settings.canUseStoredAuth() && d.trustedProfileFingerprint() != "" {
			d.connectTrustedProfile()
			return
		}
		d.status = "请完成主机密钥确认并连接，随后将加入分屏。"
		return
	}
	if cloud == "" || d.cloud == nil || d.cloud.base == nil || d.cloud.token == "" {
		return
	}
	for _, account := range d.accounts {
		if account.ID != string(cloud) {
			continue
		}
		d.loadCloudAccount(account)
		intent.cloudID, intent.cloudURL, intent.cloudUser, intent.cloudTeam = account.ID, d.cloud.base.String(), d.cloudUser, d.cloud.teamID
		intent.host, intent.port, intent.user = account.Host, strconv.Itoa(account.Port), account.Username
		d.pendingWorkspaceHost = intent
		d.status = "请核对主机指纹并连接，随后将加入分屏。"
		return
	}
	d.status = "云主机已不存在，请刷新连接列表"
}
