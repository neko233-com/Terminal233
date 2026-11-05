package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) forwardView(c *ui.Context) {
	ui.Text(c, "本机端口转发").FontSize(22).Bold()
	ui.Text(c, "只监听 127.0.0.1；流量通过当前 SSH 会话转发。关闭会话会停止转发。远程目标由 SSH 服务器解析。").TextColor(c.Theme().TextMuted).Wrap()
	var session *workspaceSession
	for _, s := range d.sessions {
		if s.id == d.currentSession {
			session = s
			break
		}
	}
	if session == nil || session.shell == nil {
		ui.Text(c, "请先连接 SSH 终端并选择对应会话。").Wrap()
		return
	}
	ui.Text(c, "所属会话："+session.label).Bold().Wrap()
	ui.TextInput(c.Key("forward-local-port"), &d.forwardLocalPort).Label("本机端口").Placeholder("0 表示自动分配")
	ui.TextInput(c.Key("forward-host"), &d.forwardHost).Label("远程目标主机").Placeholder("例如 127.0.0.1")
	ui.TextInput(c.Key("forward-port"), &d.forwardPort).Label("远程目标端口").Placeholder("例如 5432")
	if ui.Button(c.Key("forward-start"), "启动转发").Disabled(d.busy || len(session.forwards) >= maxForwardsPerSession).Clicked() {
		f, err := startLocalForward(session.shell.client, session.shell.done, d.forwardLocalPort, d.forwardHost, d.forwardPort, c.Invalidate)
		if err != nil {
			d.forwardStatus = "启动失败：" + err.Error()
		} else {
			session.forwards = append(session.forwards, f)
			d.forwardStatus = "已启动：" + f.address()
		}
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
			ui.Text(c, fmt.Sprintf("%s → %s · %s", f.address(), f.target, state)).Grow(1).Wrap()
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
