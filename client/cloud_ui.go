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
	ui.Text(c, d.cloudStatus).FontSize(12)
	for _, item := range d.accounts {
		account := item
		if ui.Button(c.Key("account-"+account.ID), account.Label).Disabled(d.busy).Clicked() {
			d.settings = connectionSettings{Host: account.Host, Port: strconv.Itoa(account.Port), User: account.Username, Password: account.Password}
			d.fingerprint = ""
			d.expectedFingerprint = account.HostKeyFingerprint
			d.status = "已载入连接，请重新获取并核对主机指纹。"
		}
	}
}
