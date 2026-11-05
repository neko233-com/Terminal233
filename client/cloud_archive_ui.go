package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type archiveImportResult struct {
	imported                int
	accounts                []cloudAccount
	preflightErr            error
	mutationErr, refreshErr error
	cancelled               bool
	preflightDone           bool
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
	for _, item := range items {
		if ctx.Err() != nil {
			result.cancelled = true
			break
		}
		item.ID = ""
		item.ExpectedUpdatedAt = ""
		if _, err := c.saveReturningID(item); err != nil {
			result.mutationErr = err
			break
		}
		result.imported++
	}
	result.accounts, result.refreshErr = c.list()
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
				data, err = encodeCloudArchive(accounts, password)
				if err == nil {
					err = writeCloudArchive(path, data)
					count = len(accounts)
				}
			}
		} else {
			var items []cloudAccount
			items, err = readCloudArchive(path, password)
			if err == nil {
				imported = importCloudAccounts(ctx, client, items)
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
	d.confirmCloudDelete = ""
	d.confirmCloudDeleteVersion = ""
	if result.mutationErr != nil {
		d.cloudListStale = true
		d.archiveStatus = fmt.Sprintf("已确认导入 %d 条；下一条请求未确认，可能已生效。请刷新并核对后再操作：%v", result.imported, result.mutationErr)
		return
	}
	if result.refreshErr != nil {
		d.cloudListStale = true
		d.archiveStatus = fmt.Sprintf("已导入 %d 条，但列表刷新失败；请先刷新：%v", result.imported, result.refreshErr)
		return
	}
	d.cloudListStale = false
	if result.cancelled {
		d.archiveStatus = fmt.Sprintf("导入已取消，已追加 %d 条连接。", result.imported)
	} else {
		d.archiveStatus = fmt.Sprintf("已追加导入 %d 条连接。", result.imported)
	}
}

func (d *desktop) cloudArchiveView(c *ui.Context) {
	ui.Text(c, "迁移当前空间的密码连接；备份使用单独密码加密，不包含私钥。导入仅追加，可能产生重复。").FontSize(12).Wrap()
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
