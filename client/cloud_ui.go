package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"strconv"
)

func (d *desktop) cloudLogin(register bool) {
	if d.busy {
		return
	}
	client, err := newCloudClient(d.cloudURL)
	if err != nil {
		d.cloudStatus = err.Error()
		return
	}
	user, password, registration := d.cloudUser, d.cloudPassword, d.cloudRegistration
	d.cloudPassword = ""
	d.cloudRegistration = ""
	d.busy = true
	d.cloudStatus = "正在连接同步服务…"
	go func() {
		var err error
		if register {
			err = client.register(user, password, registration)
		}
		if err == nil {
			err = client.login(user, password)
		}
		var accounts []cloudAccount
		if err == nil {
			accounts, err = client.list()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.cloudStatus = fmt.Sprintf("同步服务：%v", err)
				return
			}
			d.cloud = client
			d.accounts = accounts
			d.cloudStatus = "同步服务已登录"
		})
	}()
}
func (d *desktop) cloudRefresh() {
	if d.busy || d.cloud == nil {
		return
	}
	d.busy = true
	client := d.cloud
	go func() {
		accounts, err := client.list()
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.cloudStatus = err.Error()
				return
			}
			d.accounts = accounts
			d.cloudStatus = "已刷新连接列表"
		})
	}()
}
func (d *desktop) cloudSave() {
	if d.busy || d.cloud == nil || d.active.Password == "" {
		return
	}
	active := d.active
	port, err := strconv.Atoi(active.Port)
	if err != nil {
		d.cloudStatus = "端口无效"
		return
	}
	account := cloudAccount{Label: active.User + "@" + active.Host, Host: active.Host, Port: port, Username: active.User, Password: active.Password, HostKeyFingerprint: active.Fingerprint}
	for _, existing := range d.accounts {
		if existing.Host == account.Host && existing.Port == account.Port && existing.Username == account.Username {
			account.ID = existing.ID
			break
		}
	}
	client := d.cloud
	d.busy = true
	d.cloudStatus = "正在加密传输连接信息…"
	go func() {
		err := client.save(account)
		var accounts []cloudAccount
		if err == nil {
			accounts, err = client.list()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.cloudStatus = err.Error()
				return
			}
			d.accounts = accounts
			d.cloudStatus = "连接信息已同步"
		})
	}()
}
func (d *desktop) cloudView(c *ui.Context) {
	ui.Text(c, "云同步（测试中）").Bold()
	ui.TextInput(c.Key("cloudurl"), &d.cloudURL).Label("同步服务 HTTPS 地址").Placeholder("同步服务 HTTPS 地址")
	ui.TextInput(c.Key("clouduser"), &d.cloudUser).Label("同步账号").Placeholder("同步账号")
	ui.TextInput(c.Key("cloudpassword"), &d.cloudPassword).Label("同步密码").Placeholder("同步密码").Password()
	ui.TextInput(c.Key("cloudregistration"), &d.cloudRegistration).Label("注册令牌").Placeholder("注册令牌（仅注册时需要）").Password()
	if ui.Button(c.Key("cloudlogin"), "登录同步服务").Disabled(d.busy).Clicked() {
		d.cloudLogin(false)
	}
	if ui.Button(c.Key("cloudregister"), "注册并登录").Disabled(d.busy).Clicked() {
		d.cloudLogin(true)
	}
	if ui.Button(c.Key("cloudsave"), "同步当前密码连接").Disabled(d.busy || d.cloud == nil || d.active.Password == "").Clicked() {
		d.cloudSave()
	}
	if ui.Button(c.Key("cloudrefresh"), "刷新云端连接").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.cloudRefresh()
	}
	if ui.Button(c.Key("cloudlogout"), "退出同步服务").Disabled(d.busy || d.cloud == nil).Clicked() {
		d.cloudLogout()
	}
	if ui.Button(c.Key("clouddelete"), "删除选中云端连接（需确认）").Disabled(d.busy || d.confirmCloudDelete == "").Clicked() {
		d.cloudDelete()
	}
	ui.Text(c, d.cloudStatus).FontSize(12)
	for _, item := range d.accounts {
		account := item
		row := ui.Button(c.Key("account-"+account.ID), account.Label).Disabled(d.busy)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("删除…").Chosen() {
				d.confirmCloudDelete = account.ID
				d.cloudStatus = "请点击删除按钮确认：" + account.Label
			}
		})
		if row.Clicked() {
			d.settings = connectionSettings{Host: account.Host, Port: strconv.Itoa(account.Port), User: account.Username, Password: account.Password}
			d.fingerprint = ""
			d.expectedFingerprint = account.HostKeyFingerprint
			d.status = "已载入连接，请重新获取并核对主机指纹。"
		}
	}
}

func (d *desktop) cloudDelete() {
	if d.busy || d.cloud == nil || d.confirmCloudDelete == "" {
		return
	}
	id, client := d.confirmCloudDelete, d.cloud
	d.confirmCloudDelete = ""
	d.busy = true
	go func() {
		err := client.remove(id)
		var accounts []cloudAccount
		if err == nil {
			accounts, err = client.list()
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.cloudStatus = err.Error()
				return
			}
			d.accounts = accounts
			d.cloudStatus = "云端连接已删除"
		})
	}()
}
func (d *desktop) cloudLogout() {
	if d.busy || d.cloud == nil {
		return
	}
	client := d.cloud
	d.busy = true
	go func() {
		err := client.logout()
		d.win.Update(func() {
			d.busy = false
			d.cloud = nil
			d.accounts = nil
			d.confirmCloudDelete = ""
			if err != nil {
				d.cloudStatus = "本地已退出，服务端注销失败：" + err.Error()
			} else {
				d.cloudStatus = "已退出同步服务"
			}
		})
	}()
}
