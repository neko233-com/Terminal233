package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (d *desktop) knownHostsView(c *ui.Context) {
	ui.Text(c, "已知主机").FontSize(22).Bold()
	ui.TextInput(c.Key("known-host-search"), &d.knownHostSearch).Label("搜索已知主机").Placeholder("搜索地址或指纹").OnChange(func() { d.knownHostConfirm = "" }).FillWidth().MaxWidth(560)
	if d.knownHostStatus != "" {
		ui.Text(c, d.knownHostStatus).FontSize(12).Wrap()
	}
	query := strings.ToLower(strings.TrimSpace(d.knownHostSearch))
	shown := 0
	for _, entry := range d.knownHosts() {
		e := entry
		address := net.JoinHostPort(e.Key.Host, e.Key.Port)
		if query != "" && !strings.Contains(strings.ToLower(address+" "+e.Key.Fingerprint), query) {
			continue
		}
		shown++
		id := knownHostKeyID(e.Key)
		ui.Column(c.Key("known-host-" + id)).FillWidth().MaxWidth(760).Padding(14).Gap(7).Background(c.Theme().Surface).Radius(8).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(3).Children(func() {
					ui.Text(c, address).Bold().Wrap()
					ui.Text(c, fmt.Sprintf("%d 个已保存主机 · %d 个当前会话", e.Profiles, e.Sessions)).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
				})
				if ui.Button(c.Key("known-host-copy-"+id), "复制").Label("复制指纹：" + address).Tooltip("复制指纹").Clicked() {
					mygo.Clipboard.WriteText(e.Key.Fingerprint)
					d.knownHostStatus = "已复制指纹"
				}
				label := "撤销信任"
				if d.knownHostConfirm == id {
					label = "确认撤销"
				}
				if ui.Button(c.Key("known-host-forget-"+id), label).Label(label + "：" + address).Disabled(d.busy).Clicked() {
					if d.knownHostConfirm != id {
						d.knownHostConfirm = id
					} else if err := d.forgetKnownHost(e.Key); err != nil {
						d.knownHostStatus = "撤销失败：" + err.Error()
						d.knownHostConfirm = ""
					} else {
						d.knownHostStatus = "已撤销信任；下次连接需要重新核对指纹。"
						d.knownHostConfirm = ""
					}
				}
				if d.knownHostConfirm == id {
					if ui.Button(c.Key("known-host-cancel-"+id), "取消").Label("取消撤销：" + address).Clicked() {
						d.knownHostConfirm = ""
					}
				}
			})
			ui.Text(c, e.Key.Fingerprint).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			if d.knownHostConfirm == id {
				ui.Text(c, "当前连接不会关闭；重新连接前需要核对指纹。关联的独立转发将停止。").FontSize(12).Wrap()
			}
		})
	}
	if shown == 0 {
		if query == "" {
			ui.Text(c, "还没有已核对的主机指纹").TextColor(c.Theme().TextMuted)
		} else {
			ui.Text(c, "没有匹配的主机").TextColor(c.Theme().TextMuted)
		}
	}
}
