package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type archiveImportResult struct {
	imported                      int
	createdIDs                    []string
	accounts                      []cloudAccount
	preflightErr                  error
	mutationErr, refreshErr       error
	groupErr                      error
	groupState                    cloudGroupSnapshot
	groupsIncluded, groupsApplied bool
	cancelled                     bool
	preflightDone                 bool
}

// Every entry is created with a fresh ID. A failed request may have committed,
// so stop immediately and require the user to inspect the cloud list.
func importCloudAccounts(ctx context.Context, c *cloudClient, items []cloudAccount) archiveImportResult {
	result := archiveImportResult{}
	if ctx.Err() != nil {
		result.cancelled = true
		return result
	}
	existing, err := c.list()
	if err != nil {
		result.preflightErr = err
		return result
	}
	if len(existing)+len(items) > maxCloudArchiveAccounts {
		result.preflightErr = fmt.Errorf("当前空间最多 %d 条连接，剩余容量不足", maxCloudArchiveAccounts)
		return result
	}
	result.preflightDone = true
	seen := make(map[string]bool, len(existing))
	for _, a := range existing {
		seen[a.ID] = true
	}
	for _, item := range items {
		if ctx.Err() != nil {
			result.cancelled = true
			break
		}
		item.ID = ""
		item.ExpectedUpdatedAt = ""
		id, err := c.saveReturningID(item)
		if err != nil {
			result.mutationErr = err
			break
		}
		if seen[id] {
			result.mutationErr = fmt.Errorf("服务返回重复的连接编号")
			break
		}
		seen[id] = true
		result.createdIDs = append(result.createdIDs, id)
		result.imported++
	}
	result.accounts, result.refreshErr = c.list()
	return result
}

func importCloudArchive(ctx context.Context, c *cloudClient, doc cloudArchiveDocument) archiveImportResult {
	return importCloudArchiveExpected(ctx, c, doc, "")
}

func importCloudArchiveExpected(ctx context.Context, c *cloudClient, doc cloudArchiveDocument, expected string) archiveImportResult {
	if doc.Version != 2 {
		return importCloudAccounts(ctx, c, doc.Accounts)
	}
	if !validCloudArchiveGroups(doc) {
		return archiveImportResult{groupsIncluded: true, preflightErr: fmt.Errorf("备份分组无效")}
	}
	if !c.cloudGroups {
		return archiveImportResult{groupsIncluded: true, preflightErr: fmt.Errorf("目标服务不支持备份中的云端分组")}
	}
	if ctx.Err() != nil {
		return archiveImportResult{groupsIncluded: true, cancelled: true}
	}
	initial, err := c.listCloudGroups()
	if err != nil {
		return archiveImportResult{groupsIncluded: true, preflightErr: err}
	}
	if expected != "" && initial.Revision != expected {
		return archiveImportResult{groupsIncluded: true, preflightErr: fmt.Errorf("云端分组已变化，请刷新后重试")}
	}
	knownGroups := make(map[string]bool, len(initial.Groups))
	for _, g := range initial.Groups {
		knownGroups[g] = true
	}
	for _, g := range doc.Groups {
		knownGroups[g] = true
	}
	if len(knownGroups) > 1000 {
		return archiveImportResult{groupsIncluded: true, preflightErr: fmt.Errorf("合并后的分组数量超过上限")}
	}
	result := importCloudAccounts(ctx, c, doc.Accounts)
	result.groupsIncluded = true
	if result.cancelled || result.preflightErr != nil || result.mutationErr != nil || result.refreshErr != nil {
		result.groupErr = fmt.Errorf("云端分组未恢复")
		return result
	}
	if ctx.Err() != nil {
		result.cancelled = true
		result.groupErr = fmt.Errorf("已取消，云端分组未恢复")
		return result
	}
	state, err := c.listCloudGroups()
	if err != nil {
		result.groupErr = err
		return result
	}
	if !cloudGroupAccountsMatch(state, result.accounts) {
		result.groupErr = fmt.Errorf("连接列表与云端分组不一致")
		return result
	}
	if expected != "" {
		if len(initial.Groups) != len(state.Groups) {
			result.groupErr = fmt.Errorf("复制期间云端分组已变化")
			return result
		}
		for i, g := range initial.Groups {
			if state.Groups[i] != g {
				result.groupErr = fmt.Errorf("复制期间云端分组已变化")
				return result
			}
		}
		ids := make(map[string]bool, len(state.AccountIDs))
		for _, id := range state.AccountIDs {
			ids[id] = true
		}
		for _, id := range initial.AccountIDs {
			if !ids[id] || initial.Assignments[id] != state.Assignments[id] {
				result.groupErr = fmt.Errorf("复制期间连接分组已变化")
				return result
			}
		}
		for _, id := range result.createdIDs {
			if state.Assignments[id] != "" {
				result.groupErr = fmt.Errorf("新连接已被其他设备分组")
				return result
			}
		}
	}
	known := make(map[string]bool, len(state.Groups))
	for _, g := range state.Groups {
		known[g] = true
	}
	for _, g := range doc.Groups {
		if !known[g] {
			state.Groups = append(state.Groups, g)
			known[g] = true
		}
	}
	if state.Assignments == nil {
		state.Assignments = make(map[string]string)
	}
	for i, id := range result.createdIDs {
		if doc.AccountGroups[i] != "" {
			state.Assignments[id] = doc.AccountGroups[i]
		}
	}
	if !validCloudGroupSnapshot(state) {
		result.groupErr = fmt.Errorf("合并后分组无效或超过上限")
		return result
	}
	if ctx.Err() != nil {
		result.cancelled = true
		result.groupErr = fmt.Errorf("已取消，云端分组未恢复")
		return result
	}
	result.groupState, result.groupErr = c.replaceCloudGroups(state)
	result.groupsApplied = result.groupErr == nil
	return result
}

func (d *desktop) cloudArchiveAction(action string) {
	if d.busy || d.cloud == nil || (action != "export" && action != "import") {
		return
	}
	if strings.TrimSpace(d.archivePath) == "" {
		d.archiveStatus = "请选择备份文件路径。"
		return
	}
	if err := validateArchivePassword(d.archivePassword); err != nil {
		d.archiveStatus = err.Error()
		return
	}
	if action == "import" {
		if !d.canWriteCloudScope() || d.cloudListStale {
			d.archiveStatus = "当前空间不可写或连接列表需要刷新。"
			return
		}
		if !d.archiveConfirm {
			d.archiveConfirm = true
			d.archiveStatus = "导入会向当前空间追加新连接，可能产生重复；再次点击确认导入。"
			return
		}
	}
	client, path, password := d.cloud, d.archivePath, d.archivePassword
	d.archivePassword, d.archiveConfirm, d.busy = "", false, true
	d.archiveStatus = "正在处理加密备份…"
	ctx, cancel := context.WithCancel(context.Background())
	if action == "import" {
		d.archiveCancel = cancel
	}
	go func() {
		defer cancel()
		var err error
		var count int
		var imported archiveImportResult
		if action == "export" {
			var accounts []cloudAccount
			accounts, err = client.list()
			if err == nil {
				var data []byte
				if client.cloudGroups {
					var groups cloudGroupSnapshot
					groups, err = client.listCloudGroups()
					if err == nil {
						data, err = encodeCloudArchiveWithGroups(accounts, password, groups)
					}
				} else {
					data, err = encodeCloudArchive(accounts, password)
				}
				if err == nil {
					err = writeCloudArchive(path, data)
					count = len(accounts)
				}
			}
		} else {
			var document cloudArchiveDocument
			document, err = readCloudArchiveDocument(path, password)
			if err == nil {
				imported = importCloudArchive(ctx, client, document)
			}
		}
		d.win.Update(func() {
			d.busy = false
			d.archiveCancel = nil
			if err != nil {
				d.archiveStatus = "备份操作失败：" + err.Error()
				return
			}
			if action == "export" {
				d.archiveStatus = fmt.Sprintf("已加密导出 %d 条连接；文件未覆盖已有内容。", count)
				return
			}
			d.finishArchiveImport(imported)
		})
	}()
}

func (d *desktop) finishArchiveImport(result archiveImportResult) {
	d.clearCloudBatch()
	d.invalidateCloudGroups()
	if result.cancelled && !result.preflightDone {
		d.archiveStatus = "导入已取消，未写入连接。"
		return
	}
	if result.preflightErr != nil {
		d.cloudListStale = true
		d.archiveStatus = "未开始导入，请刷新并检查目标空间：" + result.preflightErr.Error()
		return
	}
	if result.refreshErr == nil {
		d.accounts = result.accounts
	}
	if result.groupsApplied {
		d.setCloudGroupState(result.groupState, nil)
	}
	d.confirmCloudDelete = ""
	d.confirmCloudDeleteVersion = ""
	if result.mutationErr != nil {
		d.cloudListStale = true
		d.archiveStatus = fmt.Sprintf("已确认导入 %d 条；下一条请求未确认，可能已生效。请刷新并核对后再操作：%v", result.imported, result.mutationErr)
		if result.groupsIncluded {
			d.archiveStatus += "；云端分组未恢复。"
		}
		return
	}
	if result.refreshErr != nil {
		d.cloudListStale = true
		d.archiveStatus = fmt.Sprintf("已导入 %d 条，但列表刷新失败；请先刷新：%v", result.imported, result.refreshErr)
		return
	}
	if result.groupErr != nil {
		d.cloudListStale = true
		d.archiveStatus = fmt.Sprintf("已确认导入 %d 条；云端分组未确认或未恢复，请刷新核对：%v", result.imported, result.groupErr)
		return
	}
	d.cloudListStale = false
	if result.cancelled {
		d.archiveStatus = fmt.Sprintf("导入已取消，已追加 %d 条连接。", result.imported)
	} else {
		d.archiveStatus = fmt.Sprintf("已追加导入 %d 条连接。", result.imported)
		if result.groupsApplied {
			d.archiveStatus += "云端分组已恢复。"
			if d.cloudGroupsStale {
				d.archiveStatus += "连接列表已变化，请刷新。"
			}
		}
	}
}

func (d *desktop) cloudArchiveView(c *ui.Context) {
	message := "迁移当前空间的密码连接；备份使用单独密码加密，不包含私钥。导入仅追加，可能产生重复。"
	if d.cloud != nil && d.cloud.cloudGroups {
		message += "支持的服务会同时备份云端分组。"
	}
	ui.Text(c, message).FontSize(12).Wrap()
	ui.TextInput(c.Key("archive-path"), &d.archivePath).Label("连接备份路径").Placeholder("选择加密备份文件").OnChange(func() { d.archiveConfirm = false })
	ui.TextInput(c.Key("archive-password"), &d.archivePassword).Label("备份密码").Placeholder("至少 12 字节，与同步账号密码分开").Password().OnChange(func() { d.archiveConfirm = false })
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if ui.Button(c.Key("archive-pick-open"), "选择待导入连接备份…").Disabled(d.busy || d.cloud == nil).Clicked() {
			d.pickLocalPath("archive-open")
		}
		if ui.Button(c.Key("archive-pick-save"), "选择导出位置…").Disabled(d.busy || d.cloud == nil).Clicked() {
			d.pickLocalPath("archive-save")
		}
		if ui.Button(c.Key("archive-export"), "加密导出当前空间").Disabled(d.busy || d.cloud == nil).Clicked() {
			d.cloudArchiveAction("export")
		}
		label := "追加导入连接…"
		if d.archiveConfirm {
			label = "确认追加导入"
		}
		if ui.Button(c.Key("archive-import"), label).Disabled(d.busy || !d.canWriteCloudScope() || d.cloudListStale).Clicked() {
			d.cloudArchiveAction("import")
		}
		if d.archiveCancel != nil && ui.Button(c.Key("archive-cancel"), "取消导入").Clicked() {
			d.archiveCancel()
			d.archiveStatus = "正在取消，等待当前请求结束…"
		}
	})
	if d.archiveStatus != "" {
		ui.Text(c, d.archiveStatus).FontSize(12).Wrap()
	}
}
