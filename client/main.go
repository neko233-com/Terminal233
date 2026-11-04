package main

import (
	"fmt"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type desktop struct {
	win                 *mygo.Window
	settings            connectionSettings
	fingerprint, status string
	busy                bool
	term                *terminal.Terminal
}

func (d *desktop) inspect() {
	if d.busy {
		return
	}
	d.busy = true
	d.fingerprint = ""
	d.status = "正在获取服务器指纹…"
	settings := d.settings
	go func() {
		fp, err := inspectHost(settings)
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = fmt.Sprintf("获取指纹失败：%v", err)
				return
			}
			if d.settings.Host != settings.Host || d.settings.Port != settings.Port || d.settings.User != settings.User {
				d.status = "连接信息已改变，请重新获取指纹。"
				return
			}
			d.fingerprint = fp
			d.status = "请通过可信渠道核对指纹，再点击确认并连接。"
		})
	}()
}

func (d *desktop) connect() {
	if d.busy || d.fingerprint == "" {
		return
	}
	d.busy = true
	d.status = "正在建立安全连接…"
	settings := d.settings
	settings.Fingerprint = d.fingerprint
	d.settings.Password = ""
	d.settings.Passphrase = ""
	go func() {
		shell, err := connectShell(settings)
		var term *terminal.Terminal
		if err == nil {
			term, err = terminal.New(terminal.Options{Conn: shell, NoClipboardWrite: true, ImageLimit: -1})
			if err != nil {
				_ = shell.Close()
			}
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.status = fmt.Sprintf("连接失败：%v", err)
				return
			}
			if d.term != nil {
				_ = d.term.Close()
			}
			d.term = term
			d.status = "已连接"
		})
	}()
}

func (d *desktop) view(c *ui.Context) {
	ui.Row(c).Fill().Children(func() {
		ui.Column(c).Width(320).Padding(18).Gap(8).Children(func() {
			ui.Text(c, "Terminal233").FontSize(23).Bold()
			ui.Text(c, "SSH 连接").Bold()
			ui.TextInput(c.Key("host"), &d.settings.Host).Label("主机地址").Placeholder("主机地址").OnChange(func() { d.fingerprint = "" })
			ui.TextInput(c.Key("port"), &d.settings.Port).Label("端口").Placeholder("端口").OnChange(func() { d.fingerprint = "" })
			ui.TextInput(c.Key("user"), &d.settings.User).Label("用户名").Placeholder("用户名").OnChange(func() { d.fingerprint = "" })
			ui.TextInput(c.Key("password"), &d.settings.Password).Label("SSH 密码").Placeholder("SSH 密码").Password()
			ui.TextInput(c.Key("key"), &d.settings.KeyPath).Label("私钥路径").Placeholder("私钥路径（可选）")
			ui.TextInput(c.Key("passphrase"), &d.settings.Passphrase).Label("私钥口令").Placeholder("私钥口令（可选）").Password()
			if ui.Button(c.Key("inspect"), "获取主机指纹").Disabled(d.busy).Clicked() {
				d.inspect()
			}
			if d.fingerprint != "" {
				ui.Text(c, d.fingerprint).FontSize(12)
			}
			if ui.PrimaryButton(c.Key("connect"), "确认指纹并连接").Disabled(d.busy || d.fingerprint == "").Clicked() {
				d.connect()
			}
			if ui.Button(c.Key("disconnect"), "断开").Disabled(d.term == nil).Clicked() {
				_ = d.term.Close()
				d.term = nil
				d.status = "已断开"
			}
			ui.Text(c, d.status).FontSize(13)
		})
		ui.Column(c).Grow(1).FillHeight().Children(func() {
			if d.term == nil {
				ui.Text(c, "连接服务器后显示终端").FontSize(18)
			} else {
				terminal.View(c, d.term).Fill()
			}
		})
	})
}

func main() {
	d := &desktop{settings: connectionSettings{Port: "22"}, status: "请填写连接信息"}
	mygo.App.WhenReady(func() {
		d.win = mygo.NewWindow(mygo.WindowOptions{Title: "Terminal233", Width: 1200, Height: 760, Content: ui.View(d.view)})
		d.win.OnClosed(func() {
			if d.term != nil {
				_ = d.term.Close()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
