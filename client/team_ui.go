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
		var err error
		var urlText string
		switch action {
		case "create":
			_, err = client.createTeam(name)
		case "join":
			_, err = client.joinTeam(invite, password)
		case "invite":
			urlText, err = client.createInvite(password, hours*3600, uses)
		}
		var teams []cloudTeam
		if err == nil {
			teams, err = client.teams()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = fmt.Sprintf("团队操作失败：%v", err)
				return
			}
			d.teams = teams
			if urlText != "" {
				d.teamInviteURL = urlText
				d.teamStatus = "邀请已创建，复制链接并通过安全渠道单独发送密码。"
			} else {
				d.teamStatus = "团队列表已更新"
			}
		})
	}()
}
func (d *desktop) selectTeam(id string) {
	if d.busy || d.cloud == nil {
		return
	}
	client := d.cloud
	d.busy = true
	go func() {
		// Only change the selected scope after both reads succeed.
		next := *client
		next.teamID = id
		accounts, err := next.list()
		var members []cloudTeamMember
		if err == nil && id != "" {
			members, err = next.members()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.applyCloudScope(id, accounts, members)
			d.teamStatus = "已切换同步空间"
		})
	}()
}

// Pending confirmations and invitation links belong to the previous scope.
func (d *desktop) applyCloudScope(id string, accounts []cloudAccount, members []cloudTeamMember) {
	d.cloud.teamID = id
	d.accounts, d.members = accounts, members
	d.teamInvites = nil
	d.confirmInvite, d.confirmMember, d.confirmCloudDelete = "", "", ""
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
	if d.busy || d.cloud == nil || d.cloud.teamID == "" || d.confirmMember == "" {
		return
	}
	c := d.cloud
	user := d.confirmMember
	d.confirmMember = ""
	d.busy = true
	go func() {
		err := c.removeMember(user)
		var m []cloudTeamMember
		if err == nil {
			m, err = c.members()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.members = m
			d.teamStatus = "成员已移除"
		})
	}()
}
func (d *desktop) teamView(c *ui.Context) {
	ui.Text(c, "团队共享").Bold()
	if d.cloud != nil {
		ui.Text(c, "当前同步空间："+d.cloudScopeName()).FontSize(12).Wrap()
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
	if ui.Button(c.Key("team-invite"), "创建团队邀请").Disabled(d.busy || d.cloud == nil || d.cloud.teamID == "").Clicked() {
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
		row := ui.Button(c.Key("member-"+m.Username), m.Username+"（"+m.Role+"）")
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("移除成员…").Chosen() {
				d.confirmMember = member.Username
				d.teamStatus = "再次确认移除：" + member.Username
			}
		})
	}
	if ui.Button(c.Key("member-admin"), "设为团队管理员").Disabled(d.busy || d.confirmMember == "").Clicked() {
		d.changeMemberRole("admin")
	}
	if ui.Button(c.Key("member-demote"), "设为普通成员").Disabled(d.busy || d.confirmMember == "").Clicked() {
		d.changeMemberRole("member")
	}
	if ui.Button(c.Key("member-remove"), "确认移除成员").Disabled(d.busy || d.confirmMember == "").Clicked() {
		d.removeMember()
	}
	if ui.Button(c.Key("team-invites"), "查看有效邀请").Disabled(d.busy || d.cloud == nil || d.cloud.teamID == "").Clicked() {
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
	if ui.Button(c.Key("invite-revoke"), "确认撤销邀请").Disabled(d.busy || d.confirmInvite == "").Clicked() {
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
	d.teamInvitePassword = ""
	d.busy = true
	d.teamStatus = "正在注册并加入团队…"
	go func() {
		err := c.registerWithInvite(user, password, token, invitePassword)
		if err == nil {
			err = c.login(user, password)
		}
		var teams []cloudTeam
		var accounts []cloudAccount
		var members []cloudTeamMember
		if err == nil {
			teams, err = c.teams()
		}
		if err == nil && len(teams) == 1 {
			c.teamID = teams[0].ID
			members, err = c.members()
		}
		if err == nil {
			accounts, err = c.list()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.cloud = c
			d.teams = teams
			d.accounts = accounts
			d.members = members
			d.teamInviteInput = ""
			d.teamStatus = "已注册并加入团队"
		})
	}()
}

func (d *desktop) changeMemberRole(role string) {
	if d.busy || d.cloud == nil || d.confirmMember == "" {
		return
	}
	c, user := d.cloud, d.confirmMember
	d.confirmMember = ""
	d.busy = true
	go func() {
		err := c.setMemberRole(user, role)
		var members []cloudTeamMember
		if err == nil {
			members, err = c.members()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.members = members
			d.teamStatus = "成员角色已更新"
		})
	}()
}
func (d *desktop) refreshInvites() {
	if d.busy || d.cloud == nil || d.cloud.teamID == "" {
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
	if d.busy || d.cloud == nil || d.confirmInvite == "" {
		return
	}
	c, id := d.cloud, d.confirmInvite
	d.confirmInvite = ""
	d.busy = true
	go func() {
		err := c.revokeInvitation(id)
		var items []cloudInvite
		if err == nil {
			items, err = c.invitations()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.teamStatus = err.Error()
				return
			}
			d.teamInvites = items
			d.teamStatus = "邀请已撤销"
		})
	}()
}
