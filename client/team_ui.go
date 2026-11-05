package main

import (
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (d *desktop) teamOperation(action string) {
	if d.busy || d.cloud == nil {
		return
	}
	if action == "invite" && !d.canManageInvites() {
		d.teamStatus = "只有团队所有者或管理员可以管理邀请。"
		return
	}
	client := d.cloud
	name, password, invite := d.teamName, d.teamInvitePassword, d.teamInviteInput
	hours, e1 := strconv.Atoi(d.teamInviteHours)
	uses, e2 := strconv.Atoi(d.teamInviteUses)
	if action == "invite" && (e1 != nil || e2 != nil || hours < 1 || hours > 168 || uses < 1 || uses > 100) {
		d.teamStatus = "邀请有效期为 1–168 小时，次数为 1–100。"
		return
	}
	if action == "join" {
		invite = extractInviteToken(invite)
	}
	d.teamInvitePassword = ""
	d.busy = true
	d.teamStatus = "正在处理团队操作…"
	go func() {
		var inviteURL string
		teams, mutationErr, refreshErr := mutateAndRefresh(func() error {
			switch action {
			case "create":
				_, err := client.createTeam(name)
				return err
			case "join":
				_, err := client.joinTeam(invite, password)
				return err
			case "invite":
				var err error
				inviteURL, err = client.createInvite(password, hours*3600, uses)
				return err
			default:
				return fmt.Errorf("未知团队操作")
			}
		}, client.teams)
		d.win.Update(func() { d.busy = false; d.finishTeamOperation(action, inviteURL, teams, mutationErr, refreshErr) })
	}()
}
func (d *desktop) finishTeamOperation(action, inviteURL string, teams []cloudTeam, mutationErr, refreshErr error) {
	if mutationErr != nil {
		d.teamStatus = "团队操作未确认，可能已生效；请先刷新团队或邀请列表：" + mutationErr.Error()
		return
	}
	if action == "invite" {
		if inviteURL == "" {
			d.teamStatus = "邀请已创建，但服务未返回链接；请检查有效邀请。"
		} else {
			d.teamInviteURL = inviteURL
			d.teamStatus = "邀请已创建，复制链接并通过安全渠道单独发送密码。"
		}
	} else {
		d.teamStatus = "团队操作已完成"
	}
	if refreshErr != nil {
		d.teamStatus += " 团队列表刷新失败，请手动刷新：" + refreshErr.Error()
		return
	}
	d.teams = teams
}

func (d *desktop) selectTeam(id string) {
	if d.busy || d.cloud == nil {
		return
	}
	client := d.cloud
	d.busy = true
	go func() {
		state := loadCloudScope(client, id)
		d.win.Update(func() {
			d.busy = false
			d.finishCloudScopeSelection(id, state)
		})
	}()
}

func (d *desktop) finishCloudScopeSelection(id string, state cloudScopeState) {
	if state.accountErr != nil {
		d.teamStatus = "无法切换同步空间，连接列表加载失败：" + state.accountErr.Error()
		return
	}
	d.applyCloudScope(id, state.accounts, state.members)
	d.teamStatus = "已切换同步空间"
	if state.memberErr != nil {
		d.teamStatus += "；成员列表加载失败，请重新选择团队刷新：" + state.memberErr.Error()
	}
}

// Pending confirmations and invitation links belong to the previous scope.
func (d *desktop) applyCloudScope(id string, accounts []cloudAccount, members []cloudTeamMember) {
	d.cloud.teamID = id
	d.cloudListStale = false
	d.accounts, d.members = accounts, members
	d.teamInvites = nil
	d.confirmInvite, d.confirmMember, d.selectedMember, d.confirmCloudDelete = "", "", "", ""
	d.teamInviteURL = ""
}

func (d *desktop) cloudScopeName() string {
	if d.cloud == nil || d.cloud.teamID == "" {
		return "个人空间"
	}
	for _, team := range d.teams {
		if team.ID == d.cloud.teamID {
			return team.Name
		}
	}
	return "团队空间"
}

func (d *desktop) currentTeamRole() string {
	if d.cloud == nil || d.cloud.teamID == "" {
		return ""
	}
	for _, team := range d.teams {
		if team.ID == d.cloud.teamID && !team.Disabled {
			return team.Role
		}
	}
	return ""
}

func (d *desktop) canManageInvites() bool {
	role := d.currentTeamRole()
	return role == "owner" || role == "admin"
}

func (d *desktop) canWriteCloudScope() bool {
	if d.cloud == nil {
		return false
	}
	if d.cloud.teamID == "" {
		return true
	}
	return d.canManageInvites()
}

func (d *desktop) selectedMemberRole() string {
	for _, member := range d.members {
		if member.Username == d.selectedMember {
			return member.Role
		}
	}
	return ""
}

func (d *desktop) canManageSelectedMember() bool {
	role := d.selectedMemberRole()
	return d.currentTeamRole() == "owner" && (role == "admin" || role == "member")
}
func (d *desktop) refreshTeams() {
	if d.busy || d.cloud == nil {
		return
	}
	c := d.cloud
	d.busy = true
	go func() {
		teams, err := c.teams()
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.teams = teams
			d.teamStatus = "团队列表已刷新"
		})
	}()
}
func (d *desktop) removeMember() {
	if d.busy || !d.canManageSelectedMember() || d.confirmMember == "" || d.confirmMember != d.selectedMember {
		return
	}
	c := d.cloud
	user := d.confirmMember
	d.confirmMember = ""
	d.selectedMember = ""
	d.busy = true
	go func() {
		items, mutationErr, refreshErr := mutateAndRefresh(func() error { return c.removeMember(user) }, c.members)
		d.win.Update(func() {
			d.busy = false
			if mutationErr != nil {
				d.members = nil
				d.teamStatus = "成员操作未确认，可能已生效；请重新选择团队以刷新成员列表：" + mutationErr.Error()
				return
			}
			if refreshErr != nil {
				d.members = nil
				d.teamStatus = "成员已移除，但列表刷新失败；请重新选择团队以刷新成员列表：" + refreshErr.Error()
				return
			}
			d.members = items
			d.teamStatus = "成员已移除"
		})
	}()
}
func (d *desktop) teamView(c *ui.Context) {
	ui.Text(c, "团队共享").Bold()
	if d.cloud != nil {
		ui.Text(c, "当前同步空间："+d.cloudScopeName()).FontSize(12).Wrap()
		if d.cloud.teamID != "" {
			role := map[string]string{"owner": "所有者", "admin": "管理员", "member": "普通成员"}[d.currentTeamRole()]
			if role == "" {
				role = "未知，请刷新团队列表"
			}
			ui.Text(c, "当前团队角色："+role).FontSize(12).Wrap()
		}
	}
	ui.Text(c, "邀请注册：填写上方云账号和密码，再粘贴邀请链接并输入邀请密码。").FontSize(12).Wrap()
	ui.TextInput(c.Key("team-name"), &d.teamName).Label("团队名称").Placeholder("新团队名称")
	if ui.Button(c.Key("team-create"), "创建团队").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.teamOperation("create")
	}
	if ui.Button(c.Key("team-refresh"), "刷新团队").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.refreshTeams()
	}
	if ui.Button(c.Key("team-personal"), "切换个人空间").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.selectTeam("")
	}
	for _, entry := range d.teams {
		team := entry
		label := team.Name + "（" + team.Role + "）"
		if d.cloud != nil && d.cloud.teamID == team.ID {
			label += " · 当前"
		}
		if team.Disabled {
			label += "（已停用）"
		}
		if ui.Button(c.Key("team-"+team.ID), label).Disabled(d.busy || team.Disabled).Clicked() {
			d.selectTeam(team.ID)
		}
	}
	ui.TextInput(c.Key("team-invite-input"), &d.teamInviteInput).Label("邀请链接或令牌").Placeholder("邀请链接或令牌")
	ui.TextInput(c.Key("team-invite-password"), &d.teamInvitePassword).Label("邀请密码").Placeholder("邀请密码（至少 12 字符）").Password()
	if ui.Button(c.Key("team-register"), "通过邀请注册并加入").Disabled(d.busy || d.cloud != nil).Clicked() {
		d.registerWithInvite()
	}
	if ui.Button(c.Key("team-join"), "加入团队").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.teamOperation("join")
	}
	ui.TextInput(c.Key("team-invite-hours"), &d.teamInviteHours).Label("邀请有效小时数").Placeholder("有效小时数")
	ui.TextInput(c.Key("team-invite-uses"), &d.teamInviteUses).Label("邀请使用次数").Placeholder("使用次数")
	if ui.Button(c.Key("team-invite"), "创建团队邀请").Disabled(d.busy || !d.canManageInvites()).Clicked() {
		d.teamOperation("invite")
	}
	if d.teamInviteURL != "" {
		ui.Text(c, d.teamInviteURL).FontSize(11).Wrap()
		if ui.Button(c.Key("team-copy"), "复制邀请链接").Clicked() {
			mygo.Clipboard.WriteText(d.teamInviteURL)
			d.teamStatus = "邀请链接已复制"
		}
	}
	for _, m := range d.members {
		member := m
		role := map[string]string{"owner": "所有者", "admin": "管理员", "member": "普通成员"}[m.Role]
		if role == "" {
			role = m.Role
		}
		label := m.Username + "（" + role + "）"
		if d.selectedMember == m.Username {
			label += " · 已选择"
		}
		row := ui.Button(c.Key("member-"+m.Username), label).Disabled(d.busy)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("选择成员").Chosen() {
				d.selectedMember, d.confirmMember = member.Username, ""
				d.teamStatus = "已选择成员：" + member.Username
			}
			if d.currentTeamRole() == "owner" && member.Role != "owner" && menu.Item("移除成员…").Chosen() {
				d.selectedMember = member.Username
				d.confirmMember = member.Username
				d.teamStatus = "再次确认移除：" + member.Username
			}
		})
		if row.Clicked() {
			d.selectedMember, d.confirmMember = member.Username, ""
			d.teamStatus = "已选择成员：" + member.Username
		}
	}
	if ui.Button(c.Key("member-admin"), "设为团队管理员").Disabled(d.busy || !d.canManageSelectedMember() || d.selectedMemberRole() == "admin").Clicked() {
		d.changeMemberRole("admin")
	}
	if ui.Button(c.Key("member-demote"), "设为普通成员").Disabled(d.busy || !d.canManageSelectedMember() || d.selectedMemberRole() == "member").Clicked() {
		d.changeMemberRole("member")
	}
	removeLabel := "移除选中成员…"
	if d.confirmMember != "" && d.confirmMember == d.selectedMember {
		removeLabel = "确认移除成员"
	}
	if ui.Button(c.Key("member-remove"), removeLabel).Disabled(d.busy || !d.canManageSelectedMember()).Clicked() {
		if d.confirmMember == d.selectedMember {
			d.removeMember()
		} else {
			d.confirmMember = d.selectedMember
			d.teamStatus = "再次确认移除：" + d.selectedMember
		}
	}
	if ui.Button(c.Key("team-invites"), "查看有效邀请").Disabled(d.busy || !d.canManageInvites()).Clicked() {
		d.refreshInvites()
	}
	for _, i := range d.teamInvites {
		inv := i
		label := time.Unix(inv.ExpiresAt, 0).Local().Format("01-02 15:04") + " 到期 / 剩余 " + strconv.Itoa(inv.Remaining) + " 次"
		row := ui.Button(c.Key("invite-"+inv.ID), label)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("撤销邀请…").Chosen() {
				d.confirmInvite = inv.ID
				d.teamStatus = "请确认撤销选中邀请"
			}
		})
	}
	if ui.Button(c.Key("invite-revoke"), "确认撤销邀请").Disabled(d.busy || !d.canManageInvites() || d.confirmInvite == "").Clicked() {
		d.revokeInvite()
	}
	ui.Text(c, strings.TrimSpace(d.teamStatus)).FontSize(12)
}

func extractInviteToken(invite string) string {
	invite = strings.TrimSpace(invite)
	if parsed, err := url.Parse(invite); err == nil && parsed.Fragment != "" {
		values, _ := url.ParseQuery(parsed.Fragment)
		return values.Get("join")
	}
	return invite
}
func (d *desktop) registerWithInvite() {
	if d.busy || d.cloud != nil {
		return
	}
	c, err := newCloudClient(d.cloudURL)
	if err != nil {
		d.teamStatus = err.Error()
		return
	}
	user, password, token, invitePassword := d.cloudUser, d.cloudPassword, extractInviteToken(d.teamInviteInput), d.teamInvitePassword
	d.cloudPassword = ""
	d.cloudRegistration, d.cloudRegistrationLocal = "", false
	d.teamInvitePassword = ""
	d.busy = true
	d.teamStatus = "正在注册并加入团队…"
	go func() {
		err := c.registerWithInvite(user, password, token, invitePassword)
		registered := err == nil
		if err == nil {
			err = c.login(user, password)
		}
		var loaded cloudInitialState
		if err == nil {
			loaded = loadCloudInitialState(c, true)
		}
		d.win.Update(func() {
			d.busy = false
			if registered {
				d.teamInviteInput = ""
			}
			if err != nil {
				if registered {
					d.teamStatus = "注册并加入团队已完成，但登录失败；请直接登录：" + err.Error()
				} else {
					d.teamStatus = "注册请求未确认；请核对账号后再试：" + err.Error()
				}
				return
			}
			d.cloud, d.teams, d.accounts, d.members = c, loaded.teams, loaded.accounts, loaded.members
			d.cloudListStale = loaded.accountErr != nil
			d.teamStatus = "已注册并加入团队"
			if loaded.teamErr != nil {
				d.teamStatus += "；团队列表加载失败，请刷新：" + loaded.teamErr.Error()
			}
			if loaded.memberErr != nil {
				d.teamStatus += "；成员列表加载失败：" + loaded.memberErr.Error()
			}
			if loaded.accountErr != nil {
				d.cloudStatus = "已登录，但连接列表加载失败；请刷新后再修改：" + loaded.accountErr.Error()
			}
		})
	}()
}

func (d *desktop) changeMemberRole(role string) {
	if d.busy || !d.canManageSelectedMember() || (role != "admin" && role != "member") || d.selectedMemberRole() == role {
		return
	}
	c, user := d.cloud, d.selectedMember
	d.confirmMember = ""
	d.busy = true
	go func() {
		items, mutationErr, refreshErr := mutateAndRefresh(func() error { return c.setMemberRole(user, role) }, c.members)
		d.win.Update(func() {
			d.busy = false
			if mutationErr != nil {
				d.members = nil
				d.teamStatus = "成员操作未确认，可能已生效；请重新选择团队以刷新成员列表：" + mutationErr.Error()
				return
			}
			if refreshErr != nil {
				d.members = nil
				d.teamStatus = "成员角色已更新，但列表刷新失败；请重新选择团队以刷新成员列表：" + refreshErr.Error()
				return
			}
			d.members = items
			d.teamStatus = "成员角色已更新"
		})
	}()
}
func (d *desktop) refreshInvites() {
	if d.busy || !d.canManageInvites() {
		return
	}
	c := d.cloud
	d.busy = true
	go func() {
		items, err := c.invitations()
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.teamInvites = items
			d.teamStatus = "邀请列表已刷新"
		})
	}()
}
func (d *desktop) revokeInvite() {
	if d.busy || !d.canManageInvites() || d.confirmInvite == "" {
		return
	}
	c, id := d.cloud, d.confirmInvite
	d.confirmInvite = ""
	d.busy = true
	go func() {
		items, mutationErr, refreshErr := mutateAndRefresh(func() error { return c.revokeInvitation(id) }, c.invitations)
		d.win.Update(func() {
			d.busy = false
			if mutationErr != nil {
				d.teamInvites = nil
				d.teamStatus = "邀请操作未确认，可能已生效；请刷新邀请列表：" + mutationErr.Error()
				return
			}
			if refreshErr != nil {
				d.teamInvites = nil
				d.teamStatus = "邀请已撤销，但列表刷新失败；请刷新邀请列表：" + refreshErr.Error()
				return
			}
			d.teamInvites = items
			d.teamStatus = "邀请已撤销"
		})
	}()
}
