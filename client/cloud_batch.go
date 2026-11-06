package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (d *desktop) clearCloudBatch() {
	d.checkedCloud = nil
	d.confirmCloudBatchDelete = false
}

func (d *desktop) cloudAccountVisible(a cloudAccount) bool {
	if !d.cloudAccountInScope(a) || !matchesProfileTags(a.Tags, d.profileTagFilters) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(d.profileSearch))
	return query == "" || strings.Contains(strings.ToLower(a.Label+" "+a.Host+" "+a.Username+" "+strings.Join(a.Tags, " ")), query)
}

func (d *desktop) checkCloudAccount(a cloudAccount, checked bool) {
	if d.checkedCloud == nil {
		d.checkedCloud = make(map[string]cloudAccount)
	}
	if checked {
		d.checkedCloud[a.ID] = a
	} else {
		delete(d.checkedCloud, a.ID)
	}
	d.confirmCloudBatchDelete = false
}

func (d *desktop) selectVisibleCloudAccounts() {
	if d.busy || d.cloudListStale {
		return
	}
	for _, account := range d.accounts {
		if d.cloudAccountVisible(account) {
			d.checkCloudAccount(account, true)
		}
	}
}

type cloudBatchResult struct {
	completed                     int
	accounts                      []cloudAccount
	cancelled, refreshDone        bool
	preflightErr                  error
	mutationErr, refreshErr       error
	groupErr                      error
	groupState                    cloudGroupSnapshot
	groupsIncluded, groupsApplied bool
}

// A network failure can leave a request's outcome unknown. Stop immediately
// and refresh; never retry or report a partial operation as fully successful.
func deleteCloudBatch(ctx context.Context, c *cloudClient, items []cloudAccount) cloudBatchResult {
	result := cloudBatchResult{}
	for _, item := range items {
		if ctx.Err() != nil {
			result.cancelled = true
			break
		}
		if err := c.removeWithRevision(item.ID, item.UpdatedAt); err != nil {
			result.mutationErr = err
			break
		}
		result.completed++
	}
	if result.cancelled && result.completed == 0 {
		return result
	}
	result.accounts, result.refreshErr = c.list()
	result.refreshDone = true
	return result
}

func (d *desktop) cloudBatch(action string) {
	if d.busy || d.cloud == nil || d.cloudListStale || !d.canWriteCloudScope() || len(d.checkedCloud) == 0 || (action != "copy" && action != "delete") {
		return
	}
	if action == "delete" && !d.confirmCloudBatchDelete {
		return
	}
	if action == "delete" && d.cloud.capabilityUnknown {
		d.status = "无法确认服务的并发保护能力，请刷新云端连接后重试。"
		d.confirmCloudBatchDelete = false
		return
	}
	if action == "copy" && d.cloud.cloudGroups && (d.cloudGroupsStale || !cloudGroupAccountsMatch(d.cloudGroupSnapshot, d.accounts)) {
		d.status = "云端分组需要先刷新，才能复制连接。"
		return
	}
	items := make([]cloudAccount, 0, len(d.checkedCloud))
	for _, a := range d.accounts {
		if selected, ok := d.checkedCloud[a.ID]; ok {
			if action == "delete" && d.cloud.conditionalDeletes {
				if _, err := time.Parse(time.RFC3339Nano, selected.UpdatedAt); err != nil {
					d.status = "勾选的连接缺少有效版本，请刷新并重新选择。"
					d.clearCloudBatch()
					return
				}
			}
			items = append(items, selected)
		}
	}
	if len(items) != len(d.checkedCloud) {
		d.status = "勾选的云端连接已变化，请刷新并重新选择。"
		d.clearCloudBatch()
		return
	}
	client := d.cloud
	sourceGroups := d.cloudGroupSnapshot
	d.clearCloudBatch()
	d.busy = true
	d.status = "正在处理云端连接…"
	ctx, cancel := context.WithCancel(context.Background())
	d.cloudBatchCancel = cancel
	go func() {
		defer cancel()
		var result cloudBatchResult
		if action == "copy" {
			for i := range items {
				items[i].Label += "（副本）"
			}
			var copied archiveImportResult
			if client.cloudGroups {
				paths := make([]string, len(items))
				for i, a := range items {
					paths[i] = sourceGroups.Assignments[a.ID]
				}
				doc := cloudArchiveDocument{Version: 2, Accounts: items, Groups: sourceGroups.Groups, AccountGroups: paths}
				copied = importCloudArchiveExpected(ctx, client, doc, sourceGroups.Revision)
			} else {
				copied = importCloudAccounts(ctx, client, items)
			}
			result = cloudBatchResult{completed: copied.imported, accounts: copied.accounts, cancelled: copied.cancelled, refreshDone: copied.preflightDone, preflightErr: copied.preflightErr, mutationErr: copied.mutationErr, refreshErr: copied.refreshErr, groupErr: copied.groupErr, groupState: copied.groupState, groupsIncluded: copied.groupsIncluded, groupsApplied: copied.groupsApplied}
		} else {
			result = deleteCloudBatch(ctx, client, items)
		}
		d.win.Update(func() {
			d.busy = false
			d.cloudBatchCancel = nil
			d.finishCloudBatch(action, result)
		})
	}()
}

func (d *desktop) finishCloudBatch(action string, result cloudBatchResult) {
	d.invalidateCloudGroups()
	d.selectedCloudProfile = ""
	verb := "复制"
	if action == "delete" {
		verb = "删除"
	}
	if result.refreshDone && result.preflightErr == nil && result.refreshErr == nil {
		d.accounts = result.accounts
	}
	if result.groupsApplied {
		d.setCloudGroupState(result.groupState, nil)
	}
	d.confirmCloudDelete, d.confirmCloudDeleteVersion = "", ""
	if result.preflightErr != nil {
		d.cloudListStale = true
		d.status = "未开始复制；请刷新并检查目标空间：" + result.preflightErr.Error()
	} else if result.mutationErr != nil {
		d.cloudListStale = true
		var response *cloudHTTPError
		if errors.As(result.mutationErr, &response) && response.Status == http.StatusConflict && response.Code == "account_conflict" {
			d.status = fmt.Sprintf("已确认%s %d 条；下一条已被其他设备修改，未删除。请刷新并核对。", verb, result.completed)
		} else {
			d.status = fmt.Sprintf("已确认%s %d 条；下一条请求可能已生效，请刷新并核对：%v", verb, result.completed, result.mutationErr)
		}
	} else if result.refreshErr != nil {
		d.cloudListStale = true
		d.status = fmt.Sprintf("已%s %d 条，但列表刷新失败，请先刷新：%v", verb, result.completed, result.refreshErr)
	} else if result.groupErr != nil {
		d.cloudListStale = true
		d.status = fmt.Sprintf("已确认%s %d 条；分组未确认或未恢复，请刷新核对：%v", verb, result.completed, result.groupErr)
	} else if result.cancelled {
		d.cloudListStale = false
		d.status = fmt.Sprintf("已取消；已确认%s %d 条云端连接。", verb, result.completed)
	} else {
		d.cloudListStale = false
		d.status = fmt.Sprintf("已%s %d 条云端连接", verb, result.completed)
	}
	d.cloudStatus = d.status
}
