package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Worker copies do not own running sessions or forwards. The UI reconciles
// those after a successful commit, while the revision check protects the file.
func (d *desktop) localArchiveWorker() *desktop {
	return &desktop{
		profilePath: d.profilePath, profileExists: d.profileExists, profileRevision: d.profileRevision, profileFormat: d.profileFormat, profileStoreBlocked: d.profileStoreBlocked,
		profiles: append([]profile(nil), d.profiles...), identities: append([]savedIdentity(nil), d.identities...), profileGroups: append([]string(nil), d.profileGroups...), nextProfileID: d.nextProfileID,
		forwardRulesPath: d.forwardRulesPath,
	}
}

func (d *desktop) localArchiveAction(action string) {
	if d.busy || d.win == nil {
		return
	}
	if action == "apply" {
		if d.localArchivePending == nil {
			return
		}
		doc := *d.localArchivePending
		d.localArchivePending = nil
		worker := d.localArchiveWorker()
		expected := d.profileRevision
		d.busy, d.localArchiveStatus = true, "正在导入本机备份…"
		go func() {
			err := worker.importLocalArchive(doc)
			d.win.Update(func() {
				d.busy = false
				if err != nil {
					if worker.profileStoreBlocked {
						d.profileStoreBlocked = true
					}
					if worker.profileStoreConflict || d.profileRevision != expected {
						d.profileStoreConflict = true
					}
					d.localArchiveStatus = "导入失败：" + err.Error()
					return
				}
				if d.profileRevision != expected {
					d.profileStoreConflict = true
					d.localArchiveStatus = "备份已写入，但当前窗口状态已变化；请重新载入本机连接。"
					return
				}
				d.profiles, d.identities, d.profileGroups, d.nextProfileID = worker.profiles, worker.identities, worker.profileGroups, worker.nextProfileID
				d.profileRevision, d.profileExists, d.profileFormat = worker.profileRevision, worker.profileExists, worker.profileFormat
				d.forwardRules = worker.forwardRules
				d.stopChangedForwardRules()
				d.reconcileForwardRules()
				d.localArchiveStatus = fmt.Sprintf("已导入 %d 个主机、%d 个身份和 %d 条转发规则。请重新核对主机指纹。", len(doc.Profiles), len(doc.Identities), len(doc.Rules))
			})
		}()
		return
	}
	if action != "export" && action != "inspect" {
		return
	}
	path, password := strings.TrimSpace(d.localArchivePath), d.localArchivePassword
	if path == "" {
		d.localArchiveStatus = "请选择备份文件路径。"
		return
	}
	if err := validateArchivePassword(password); err != nil {
		d.localArchiveStatus = err.Error()
		return
	}
	worker := d.localArchiveWorker()
	d.localArchivePassword, d.localArchivePending, d.busy = "", nil, true
	d.localArchiveStatus = "正在处理加密备份…"
	go func() {
		var doc localArchiveDocument
		var err error
		if action == "export" {
			doc, err = worker.snapshotLocalArchive()
			if err == nil {
				var data []byte
				data, err = encodeLocalArchive(doc, password)
				if err == nil {
					err = writeCloudArchive(path, data)
				}
			}
		} else {
			doc, err = readLocalArchive(path, password)
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.localArchiveStatus = "备份操作失败：" + err.Error()
				return
			}
			if action == "export" {
				d.localArchiveStatus = fmt.Sprintf("已加密导出 %d 个主机、%d 个身份和 %d 条转发规则。", len(doc.Profiles), len(doc.Identities), len(doc.Rules))
				return
			}
			if d.workspace != "local-archive" {
				d.localArchiveStatus = "已取消导入预览"
				return
			}
			d.localArchivePending = &doc
			d.localArchiveStatus = fmt.Sprintf("待导入：%d 个主机、%d 个身份、%d 条转发规则。", len(doc.Profiles), len(doc.Identities), len(doc.Rules))
		})
	}()
}

func (d *desktop) retryLocalImportRecovery() {
	if d.busy || d.win == nil || d.profilePath == "" || d.forwardRulesPath == "" {
		return
	}
	profilesPath, rulesPath := d.profilePath, d.forwardRulesPath
	d.busy = true
	d.localArchiveStatus = "正在恢复本机数据…"
	go func() {
		_, err := recoverLocalImport(profilesPath, rulesPath)
		worker := &desktop{forwardRulesPath: rulesPath}
		if err == nil {
			err = worker.loadProfiles(profilesPath)
		}
		var rules forwardRulesFile
		if err == nil {
			rules, err = readForwardRules(rulesPath)
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.localArchiveStatus = "恢复失败，原文件已保留：" + err.Error()
				return
			}
			d.profiles, d.identities, d.profileGroups, d.nextProfileID = worker.profiles, worker.identities, worker.profileGroups, worker.nextProfileID
			d.profileRevision, d.profileExists, d.profileFormat = worker.profileRevision, worker.profileExists, worker.profileFormat
			d.profileStoreBlocked, d.profileStoreConflict = false, false
			d.forwardRules = rules.Items
			d.stopChangedForwardRules()
			d.reconcileForwardRules()
			d.localArchiveStatus = "本机数据已恢复。"
		})
	}()
}

func (d *desktop) localArchiveView(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("local-archive-back"), "‹").Label("返回主机列表").Disabled(d.busy).Clicked() {
			d.localArchivePending = nil
			d.localArchivePassword = ""
			d.workspace = "connections"
		}
		ui.Text(c, "本机导入与导出").FontSize(22).Bold()
	})
	ui.Text(c, "加密备份主机、身份与转发规则；不包含私钥文件。导入后需重新核对主机指纹。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	ui.Column(c).FillWidth().MaxWidth(620).Gap(8).Children(func() {
		if d.profileStoreBlocked && d.profilePath != "" && d.forwardRulesPath != "" {
			if ui.Button(c.Key("local-archive-recover"), "重试恢复本机数据").Disabled(d.busy).Clicked() {
				d.retryLocalImportRecovery()
			}
		}
		ui.TextInput(c.Key("local-archive-path"), &d.localArchivePath).Label("本机备份路径").Placeholder("选择加密备份文件").OnChange(func() { d.localArchivePending = nil }).FillWidth()
		ui.TextInput(c.Key("local-archive-password"), &d.localArchivePassword).Label("本机备份密码").Placeholder("至少 12 字节").Password().OnChange(func() { d.localArchivePending = nil }).FillWidth()
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.Button(c.Key("local-archive-pick-open"), "选择备份…").Disabled(d.busy).Clicked() {
				d.pickLocalPath("local-archive-open")
			}
			if ui.Button(c.Key("local-archive-pick-save"), "选择导出位置…").Disabled(d.busy).Clicked() {
				d.pickLocalPath("local-archive-save")
			}
		})
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.PrimaryButton(c.Key("local-archive-export"), "加密导出").Disabled(d.busy).Clicked() {
				d.localArchiveAction("export")
			}
			if ui.Button(c.Key("local-archive-inspect"), "预览导入").Disabled(d.busy).Clicked() {
				d.localArchiveAction("inspect")
			}
		})
		if d.localArchiveStatus != "" {
			ui.Text(c, d.localArchiveStatus).Wrap()
		}
		if d.localArchivePending != nil {
			ui.Text(c, "追加导入，不覆盖已有项目；身份重名会自动改名。私钥文件需另行迁移。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			for i, p := range d.localArchivePending.Profiles {
				if i == 5 {
					ui.Text(c, fmt.Sprintf("另有 %d 个主机", len(d.localArchivePending.Profiles)-i)).FontSize(12).TextColor(c.Theme().TextMuted)
					break
				}
				label := p.Label
				if label == "" {
					label = p.User + "@" + p.Host
				}
				ui.Text(c, label+" · "+p.Host+":"+p.Port).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c.Key("local-archive-apply"), "确认追加导入").Disabled(d.busy).Clicked() {
					d.localArchiveAction("apply")
				}
				if ui.Button(c.Key("local-archive-cancel"), "取消").Disabled(d.busy).Clicked() {
					d.localArchivePending = nil
					d.localArchiveStatus = "已取消导入"
				}
			})
		}
	})
}
