package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCloudBatchStopsAtConflictAndRefreshes(t *testing.T) {
	var deleted []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":"two"}]`))
			return
		}
		if r.Header.Get("X-Neko-Expected-Updated-At") != "2026-01-01T00:00:00Z" {
			t.Error("delete omitted the selected revision")
		}
		deleted = append(deleted, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/two") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"account_conflict","message":"changed"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token, c.conditionalDeletes = server.Client().Transport, "token", true
	rev := "2026-01-01T00:00:00Z"
	result := deleteCloudBatch(context.Background(), c, []cloudAccount{{ID: "one", UpdatedAt: rev}, {ID: "two", UpdatedAt: rev}, {ID: "three", UpdatedAt: rev}})
	if result.completed != 1 || result.mutationErr == nil || result.refreshErr != nil || len(deleted) != 2 || len(result.accounts) != 1 {
		t.Fatalf("unexpected partial result: %+v, calls %v", result, deleted)
	}
	d := &desktop{}
	d.finishCloudBatch("delete", result)
	if !d.cloudListStale || !strings.Contains(d.status, "已确认删除 1 条") || !strings.Contains(d.status, "未删除") {
		t.Fatalf("partial conflict was not explained: %s", d.status)
	}
	d.finishCloudBatch("copy", cloudBatchResult{preflightErr: errors.New("full")})
	if !strings.Contains(d.status, "未开始复制") {
		t.Fatal("preflight failure was presented as a possibly committed write")
	}
	d.accounts = []cloudAccount{{ID: "old"}}
	d.finishCloudBatch("delete", cloudBatchResult{completed: 1, refreshDone: true})
	if len(d.accounts) != 0 || d.cloudListStale {
		t.Fatal("an empty successful refresh retained a deleted connection")
	}
}

func TestCloudBatchCancelStopsBetweenRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":"two"}]`))
			return
		}
		calls++
		cancel()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "token"
	result := deleteCloudBatch(ctx, c, []cloudAccount{{ID: "one"}, {ID: "two"}})
	if !result.cancelled || result.completed != 1 || calls != 1 || !result.refreshDone || result.refreshErr != nil {
		t.Fatalf("cancellation did not stop after the in-flight request: %+v calls=%d", result, calls)
	}
	d := &desktop{}
	d.finishCloudBatch("delete", result)
	if d.cloudListStale || len(d.accounts) != 1 || !strings.Contains(d.status, "已取消；已确认删除 1 条") {
		t.Fatalf("cancellation outcome not shown: %s", d.status)
	}
	d.accounts = []cloudAccount{{ID: "original"}}
	d.finishCloudBatch("copy", cloudBatchResult{cancelled: true})
	if len(d.accounts) != 1 || d.cloudListStale {
		t.Fatal("cancel before preflight cleared existing accounts")
	}
}

func TestCloudBatchCancelButton(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &desktop{workspace: "connections", busy: true, cloudBatchCancel: cancel}
	view := ui.NewTester(d.view, 1400, 1400)
	if err := view.Click("取消云端批量操作"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || !strings.Contains(d.status, "等待当前请求") {
		t.Fatal("cancel button did not request cancellation")
	}
}

func TestCloudBatchSelectionAndScopeReset(t *testing.T) {
	c := &cloudClient{token: "token"}
	d := &desktop{workspace: "connections", profileSource: "cloud", cloud: c, accounts: []cloudAccount{{ID: "one", Label: "alpha", Host: "one"}, {ID: "two", Label: "beta", Host: "two"}}}
	view := ui.NewTester(d.view, 1400, 1400)
	if err := view.Click("更多连接操作"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("勾选云端连接：alpha"); err != nil {
		t.Fatal(err)
	}
	d.profileSearch = "beta"
	view.Frame()
	if err := view.Click("勾选当前显示的云端连接"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("已勾选 2 个云端连接（包括筛选隐藏项）") {
		t.Fatal("hidden selection not disclosed")
	}
	if err := view.Click("删除选中云端连接…"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("当前服务未确认支持并发删除保护，其他设备的更改可能被删除。") {
		t.Fatal("legacy server risk missing")
	}
	d.applyCloudScope("team", nil, nil)
	if len(d.checkedCloud) != 0 || d.confirmCloudBatchDelete {
		t.Fatal("selection survived scope change")
	}
	d.cloud.teamID = ""
	d.accounts = []cloudAccount{{ID: "one", UpdatedAt: "2026-01-01T00:00:00Z"}}
	d.checkCloudAccount(d.accounts[0], true)
	d.cloud.capabilityUnknown = true
	d.confirmCloudBatchDelete = true
	d.cloudBatch("delete")
	if d.busy || d.confirmCloudBatchDelete || !strings.Contains(d.status, "无法确认") {
		t.Fatal("unknown capability allowed batch delete")
	}
	d.finishCloudRefresh(d.accounts, nil)
	if len(d.checkedCloud) != 0 {
		t.Fatal("refresh retained a previous selection")
	}
}
