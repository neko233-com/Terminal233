package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"strings"
)

func forwardHostLabel(p profile) string {
	return fmt.Sprintf("%s · %s@%s:%s [%s]", p.Label, p.Settings.User, p.Settings.Host, p.Settings.Port, p.ID)
}

func (d *desktop) forwardSavedView(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "端口转发").FontSize(22).Bold().Grow(1)
		if ui.PrimaryButton(c.Key("forward-rule-new"), "+ 新建规则").Disabled(d.forwardRuleEditing != "" || d.busy).Clicked() {
			id, err := newRandomID()
			if err != nil {
				d.forwardRulesStatus = err.Error()
				return
			}
			d.forwardRuleEditing, d.forwardRuleOriginal = "new", nil
			d.forwardRuleDraft = savedForwardRule{ID: id, Mode: "local", ListenPort: "0", TargetHost: "127.0.0.1"}
			d.forwardRuleHostSelection = ""
			for _, session := range d.sessions {
				if session.id == d.currentSession {
					d.forwardRuleHostSelection = session.sourceProfileID
					break
				}
			}
			d.forwardRulesStatus, d.forwardRuleConfirmDelete = "", ""
		}
		if ui.Button(c.Key("forward-rules-reload"), "刷新").Disabled(d.forwardRuleEditing != "").Clicked() {
			if file, err := readForwardRules(d.forwardRulesPath); err != nil {
				d.forwardRulesStatus = "刷新失败：" + err.Error()
			} else {
				d.forwardRules, d.forwardRulesStatus = file.Items, "规则已刷新"
				d.reconcileForwardRules()
			}
		}
	})
	if d.forwardRulesStatus != "" {
		ui.Text(c, d.forwardRulesStatus).FontSize(12).Wrap()
	}
	if d.forwardRuleEditing != "" {
		d.forwardRuleEditor(c)
	}
	if len(d.forwardRules) == 0 && d.forwardRuleEditing == "" {
		ui.Text(c, "还没有转发规则").TextColor(c.Theme().TextMuted)
	}
	for _, saved := range d.forwardRules {
		rule := saved
		host, ok := d.forwardRuleHost(rule.HostID)
		label := "主机已移除"
		if ok {
			label = host.Label
			if label == "" {
				label = host.Settings.User + "@" + host.Settings.Host
			}
		}
		state := "已停止"
		run := d.savedForwardRuns[rule.ID]
		if run != nil {
			state = "正在启动"
			if run.forward != nil {
				state = "运行中 · " + run.forward.address()
			}
		}
		kind := map[string]string{"local": "本地", "remote": "远程", "dynamic": "动态 SOCKS5"}[rule.Mode]
		row := ui.Row(c.Key("forward-rule-row-" + rule.ID)).FillWidth().MaxWidth(900).Padding(12).Gap(10).AlignItems(ui.Center).Background(c.Theme().Surface).Radius(8)
		row.Children(func() {
			ui.Column(c).Grow(1).Gap(3).Children(func() {
				ui.Text(c, rule.Name).Bold()
				ui.Text(c, kind+" · "+label+" · "+state).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			})
			button := "启动"
			if run != nil {
				button = "停止"
			}
			if ui.Button(c.Key("forward-rule-toggle-"+rule.ID), button).Disabled(d.busy || d.forwardRuleEditing != "").Clicked() {
				if run != nil {
					d.stopSavedForward(rule.ID)
					d.forwardRulesStatus = "转发已停止"
				} else {
					d.startSavedForward(rule, c.Invalidate)
				}
			}
			if ui.Button(c.Key("forward-rule-edit-"+rule.ID), "编辑").Disabled(d.busy || run != nil || d.forwardRuleEditing != "").Clicked() {
				copy := rule
				d.forwardRuleOriginal = &copy
				d.forwardRuleDraft = rule
				d.forwardRuleEditing = rule.ID
				d.forwardRuleHostSelection = ""
				if ok {
					d.forwardRuleHostSelection = string(host.ID)
				}
				d.forwardRuleConfirmDelete, d.forwardRulesStatus = "", ""
			}
			remove := "删除"
			if d.forwardRuleConfirmDelete == rule.ID {
				remove = "确认删除"
			}
			if ui.Button(c.Key("forward-rule-delete-"+rule.ID), remove).Disabled(d.busy || run != nil || d.forwardRuleEditing != "").Clicked() {
				if d.forwardRuleConfirmDelete != rule.ID {
					d.forwardRuleConfirmDelete = rule.ID
				} else if err := d.deleteForwardRule(rule); err != nil {
					d.forwardRulesStatus = "删除失败：" + err.Error()
				} else {
					d.forwardRulesStatus, d.forwardRuleConfirmDelete = "规则已删除", ""
				}
			}
		})
		if row.DoubleClicked() && !d.busy && d.forwardRuleEditing == "" {
			if run != nil {
				d.stopSavedForward(rule.ID)
				d.forwardRulesStatus = "转发已停止"
			} else {
				d.startSavedForward(rule, c.Invalidate)
			}
		}
		if run != nil && run.forward != nil && run.forward.errorText() != "" {
			ui.Text(c, "最近连接失败："+run.forward.errorText()).FontSize(12).Wrap()
		}
	}
	label := "临时转发"
	if d.showTemporaryForward {
		label = "收起临时转发"
	}
	if ui.Button(c.Key("forward-temporary-toggle"), label).Clicked() {
		d.showTemporaryForward = !d.showTemporaryForward
	}
}

func (d *desktop) forwardRuleEditor(c *ui.Context) {
	r := &d.forwardRuleDraft
	ui.Column(c).FillWidth().MaxWidth(650).Padding(16).Gap(9).Background(c.Theme().Surface).Radius(10).Children(func() {
		ui.Text(c, "转发规则").FontSize(16).Bold()
		ui.TextInput(c.Key("forward-rule-name"), &r.Name).Label("规则名称").Placeholder("例如 数据库").FillWidth()
		options := []string{"选择主机"}
		selected := options[0]
		for _, p := range d.profiles {
			label := forwardHostLabel(p)
			options = append(options, label)
			if string(p.ID) == d.forwardRuleHostSelection {
				selected = label
			}
		}
		if ui.Select(c.Key("forward-rule-host"), &selected, options).Label("转发主机").FillWidth().Changed() {
			d.forwardRuleHostSelection = ""
			for _, p := range d.profiles {
				if selected == forwardHostLabel(p) {
					d.forwardRuleHostSelection = string(p.ID)
					break
				}
			}
		}
		ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
			for _, mode := range []struct{ id, label string }{{"local", "本地"}, {"remote", "远程"}, {"dynamic", "动态 SOCKS5"}} {
				if workspaceButton(c, "forward-rule-mode-"+mode.id, mode.label, r.Mode == mode.id).Clicked() {
					r.Mode = mode.id
				}
			}
		})
		listen := "本机监听端口"
		if r.Mode == "remote" {
			listen = "远程监听端口"
		}
		ui.TextInput(c.Key("forward-rule-listen"), &r.ListenPort).Label(listen).Placeholder("0 表示自动分配").FillWidth()
		if r.Mode != "dynamic" {
			label := "远程目标主机"
			if r.Mode == "remote" {
				label = "本机可访问的目标主机"
			}
			ui.TextInput(c.Key("forward-rule-target"), &r.TargetHost).Label(label).Placeholder("127.0.0.1").FillWidth()
			ui.TextInput(c.Key("forward-rule-port"), &r.TargetPort).Label("目标端口").Placeholder("例如 5432").FillWidth()
		}
		if r.Mode == "remote" {
			ui.Text(c, "请求远程监听 127.0.0.1；实际范围由 SSH 服务器配置决定。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c.Key("forward-rule-save"), "保存规则").Disabled(d.busy || d.forwardRuleHostSelection == "").Clicked() {
				r.Name, r.ListenPort = strings.TrimSpace(r.Name), strings.TrimSpace(r.ListenPort)
				if r.Mode == "dynamic" {
					r.TargetHost, r.TargetPort = "", ""
				} else {
					r.TargetHost, r.TargetPort = strings.TrimSpace(r.TargetHost), strings.TrimSpace(r.TargetPort)
				}
				check := *r
				check.HostID = strings.Repeat("0", 32)
				if err := validateForwardRule(check); err != nil {
					d.forwardRulesStatus = "保存失败：" + err.Error()
					return
				}
				id, err := d.ensureForwardHostID(profileID(d.forwardRuleHostSelection))
				if err != nil {
					d.forwardRulesStatus = "保存失败：" + err.Error()
					return
				}
				r.HostID = id
				if err := d.saveForwardRule(*r, d.forwardRuleOriginal); err != nil {
					d.forwardRulesStatus = "保存失败：" + err.Error()
				} else {
					d.forwardRuleEditing, d.forwardRuleOriginal, d.forwardRulesStatus = "", nil, "规则已保存"
				}
			}
			if ui.Button(c.Key("forward-rule-cancel"), "取消").Clicked() {
				d.forwardRuleEditing, d.forwardRuleOriginal, d.forwardRulesStatus = "", nil, ""
			}
		})
	})
}
