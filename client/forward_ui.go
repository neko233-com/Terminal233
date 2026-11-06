package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) forwardView(c *ui.Context) {
	ui.Text(c, "端口转发").FontSize(22).Bold()
	var session *workspaceSession
	for _, s := range d.sessions {
		if s.id == d.currentSession {
			session = s
			break
		}
	}
	if session == nil || session.shell == nil {
		ui.Text(c, "选择已连接的 SSH 会话，创建转发。 ").TextColor(c.Theme().TextMuted).Wrap()
		return
	}
	ui.Text(c, "SSH 主机 · "+session.label).TextColor(c.Theme().TextMuted).Wrap()
	if terminalEnded(session.term) {
		ui.Text(c, "SSH 会话已断开，请重新连接。").Wrap()
	}
	if d.forwardMode == "" {
		d.forwardMode = "本地"
	}
	ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
		for _, mode := range []struct{ key, label string }{{"local", "本地"}, {"remote", "远程"}, {"dynamic", "动态 SOCKS5"}} {
			if workspaceButton(c, "forward-mode-"+mode.key, mode.label, d.forwardMode == mode.label).Disabled(d.forwardPending).Clicked() {
				d.forwardMode, d.forwardStatus = mode.label, ""
			}
		}
	})
	portLabel := "本机监听端口"
	if d.forwardMode == "远程" {
		portLabel = "远程监听端口"
	}
	ui.TextInput(c.Key("forward-local-port"), &d.forwardLocalPort).Label(portLabel).Placeholder("0 表示自动分配")
	if d.forwardMode != "动态 SOCKS5" {
		targetLabel := "远程目标主机"
		if d.forwardMode == "远程" {
			targetLabel = "本机可访问的目标主机"
		}
		ui.TextInput(c.Key("forward-host"), &d.forwardHost).Label(targetLabel).Placeholder("例如 127.0.0.1")
		ui.TextInput(c.Key("forward-port"), &d.forwardPort).Label("目标端口").Placeholder("例如 5432")
	}
	switch d.forwardMode {
	case "本地":
		ui.Text(c, "通过 SSH 访问远程服务。本机仅监听 127.0.0.1。").TextColor(c.Theme().TextMuted).FontSize(12).Wrap()
	case "远程":
		ui.Text(c, "让远程主机访问本机服务。请求远程监听 127.0.0.1，实际范围取决于 SSH 服务器配置。").TextColor(c.Theme().TextMuted).FontSize(12).Wrap()
	case "动态 SOCKS5":
		ui.Text(c, "本机 SOCKS5 代理仅监听 127.0.0.1，目标由 SSH 服务器解析。").TextColor(c.Theme().TextMuted).FontSize(12).Wrap()
	}
	if ui.PrimaryButton(c.Key("forward-start"), "启动转发").Disabled(d.busy || d.forwardPending || terminalEnded(session.term) || len(session.forwards) >= maxForwardsPerSession).Clicked() {
		d.startForward(session, c.Invalidate)
	}
	if d.forwardStatus != "" {
		ui.Text(c, d.forwardStatus).Wrap()
	}
	for i, f := range session.forwards {
		i, f := i, f
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			state := "运行中"
			if f.isClosed() {
				state = "已停止"
			}
			kind := "本地"
			if f.mode == "remote" {
				kind = "远程"
			}
			if f.mode == "dynamic" {
				kind = "动态"
			}
			ui.Text(c, fmt.Sprintf("%s · %s → %s · %s", kind, f.address(), f.target, state)).Grow(1).Wrap()
			if ui.Button(c.Key(fmt.Sprintf("forward-stop-%d", i)), "停止并移除").Clicked() {
				f.Close()
				session.forwards = append(session.forwards[:i], session.forwards[i+1:]...)
				d.forwardStatus = "转发已停止"
			}
		})
		if message := f.errorText(); message != "" {
			ui.Text(c, "最近连接失败："+message).Wrap()
		}
	}
}

func (d *desktop) startForward(session *workspaceSession, invalidate func()) {
	mode, port, host, target := d.forwardMode, d.forwardLocalPort, d.forwardHost, d.forwardPort
	d.forwardGeneration++
	generation := d.forwardGeneration
	d.forwardPending, d.forwardStatus = true, "正在启动转发…"
	timer := time.AfterFunc(15*time.Second, func() {
		d.win.Update(func() {
			if d.forwardGeneration == generation && d.forwardPending {
				d.forwardGeneration++
				d.forwardPending = false
				d.forwardStatus = "启动超时，请检查 SSH 服务器的转发配置。"
			}
		})
	})
	go func() {
		var f *localForward
		var err error
		switch mode {
		case "远程":
			f, err = startRemoteForward(session.shell.client, session.shell.done, port, host, target, invalidate)
		case "动态 SOCKS5":
			f, err = startDynamicForward(session.shell.client, session.shell.done, port, invalidate)
		default:
			f, err = startLocalForward(session.shell.client, session.shell.done, port, host, target, invalidate)
		}
		d.win.Update(func() {
			timer.Stop()
			if d.forwardGeneration != generation || !d.forwardPending {
				if f != nil {
					f.Close()
				}
				return
			}
			d.forwardPending = false
			alive := false
			for _, s := range d.sessions {
				if s == session {
					alive = true
					break
				}
			}
			if !alive || terminalEnded(session.term) {
				if f != nil {
					f.Close()
				}
				d.forwardStatus = "会话已关闭，转发未启动。"
				return
			}
			if err != nil {
				d.forwardStatus = "启动失败：" + err.Error()
				return
			}
			session.forwards = append(session.forwards, f)
			d.forwardStatus = "已启动：" + f.address()
		})
	}()
}
