package main

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"io"
	"os"
)

func (d *desktop) adminAction(action string) {
	if d.busy || d.cloud == nil {
		return
	}
	name, path := d.adminConfirmUser, d.adminBackupPath
	teamID := d.adminConfirmTeam
	if (action == "export" || action == "import") && path == "" {
		d.adminStatus = "请输入加密备份文件路径"
		return
	}
	if action == "import" && !d.adminConfirmRestore {
		d.adminConfirmRestore = true
		d.adminStatus = "恢复将替换全部云端数据并退出所有会话；再次点击确认恢复。"
		return
	}
	snapshot := *d.cloud
	snapshot.adminTOTP = d.adminTOTP
	d.adminTOTP = ""
	c := &snapshot
	d.adminConfirmRestore = false
	d.adminConfirmUser = ""
	d.adminConfirmTeam = ""
	d.busy = true
	go func() {
		var err error
		var users []cloudAdminUser
		var teams []cloudTeam
		switch action {
		case "teams":
			teams, err = c.adminTeams()
		case "suspend-team", "enable-team":
			err = c.setTeamDisabled(teamID, action == "suspend-team")
			if err == nil {
				teams, err = c.adminTeams()
			}
		case "users":
			users, err = c.adminUsers()
		case "disable", "enable":
			err = c.setUserDisabled(name, action == "disable")
			if err == nil {
				users, err = c.adminUsers()
			}
		case "export":
			var backup cloudBackup
			backup, err = c.exportBackup()
			if err == nil {
				var data []byte
				data, err = json.Marshal(backup)
				if err == nil {
					var f *os.File
					f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
					if err == nil {
						_, err = f.Write(data)
						closeErr := f.Close()
						if err == nil {
							err = closeErr
						}
						if err != nil {
							_ = os.Remove(path)
						}
					}
				}
			}
		case "import":
			var f *os.File
			f, err = os.Open(path)
			if err == nil {
				var data []byte
				data, err = io.ReadAll(io.LimitReader(f, 24*1024*1024+1))
				_ = f.Close()
				if err == nil && len(data) > 24*1024*1024 {
					err = fmt.Errorf("备份文件过大")
				}
				if err == nil {
					var backup cloudBackup
					err = json.Unmarshal(data, &backup)
					if err == nil {
						err = c.importBackup(backup)
					}
				}
			}
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.adminStatus = fmt.Sprintf("管理操作失败：%v", err)
				return
			}
			if users != nil {
				d.adminUsers = users
			}
			if teams != nil {
				d.adminTeams = teams
			}
			if action == "import" {
				d.clearCloudState()
				d.adminStatus = "备份已恢复，请重新登录。"
			} else {
				d.adminStatus = "管理操作已完成"
			}
		})
	}()
}
func (d *desktop) adminView(c *ui.Context) {
	ui.Text(c, "服务器管理（管理员）").Bold()
	ui.TextInput(c.Key("admin-totp"), &d.adminTOTP).Label("管理员动态验证码").Placeholder("管理员 6 位动态验证码").Password()
	if ui.Button(c.Key("admin-users"), "查看用户").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.adminAction("users")
	}
	for _, entry := range d.adminUsers {
		user := entry
		label := user.Username
		if user.Disabled {
			label += "（已禁用）"
		}
		row := ui.Button(c.Key("admin-user-"+user.Username), label)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("选择此用户").Chosen() {
				d.adminConfirmUser = user.Username
				d.adminStatus = "已选择：" + user.Username
			}
		})
	}
	if d.adminConfirmUser != "" {
		ui.Text(c, "选中用户："+d.adminConfirmUser).FontSize(12)
	}
	if ui.Button(c.Key("admin-disable"), "确认禁用所选用户").Disabled(d.busy || d.adminConfirmUser == "").Clicked() {
		d.adminAction("disable")
	}
	if ui.Button(c.Key("admin-enable"), "确认启用所选用户").Disabled(d.busy || d.adminConfirmUser == "").Clicked() {
		d.adminAction("enable")
	}
	if ui.Button(c.Key("admin-teams"), "查看所有团队").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.adminAction("teams")
	}
	for _, entry := range d.adminTeams {
		team := entry
		label := team.Name
		if team.Disabled {
			label += "（已停用）"
		}
		row := ui.Button(c.Key("admin-team-"+team.ID), label)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("选择此团队").Chosen() {
				d.adminConfirmTeam = team.ID
				d.adminStatus = "已选择团队：" + team.Name
			}
		})
	}
	if ui.Button(c.Key("admin-team-disable"), "确认停用所选团队").Disabled(d.busy || d.adminConfirmTeam == "").Clicked() {
		d.adminAction("suspend-team")
	}
	if ui.Button(c.Key("admin-team-enable"), "确认启用所选团队").Disabled(d.busy || d.adminConfirmTeam == "").Clicked() {
		d.adminAction("enable-team")
	}
	ui.TextInput(c.Key("admin-backup-path"), &d.adminBackupPath).Label("加密备份路径").Placeholder("加密备份文件路径").OnChange(func() { d.adminConfirmRestore = false })
	if ui.Button(c.Key("admin-export"), "导出加密备份").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.adminAction("export")
	}
	if ui.Button(c.Key("admin-import"), "恢复加密备份（需确认）").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.adminAction("import")
	}
	ui.Text(c, d.adminStatus).FontSize(12).Wrap()
}
