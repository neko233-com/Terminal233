package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// A portable template never carries device paths, credentials or host-key trust.
type cloudWorkspaceEntry struct {
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	TabName     string `json:"tabName,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        string `json:"port,omitempty"`
	User        string `json:"user,omitempty"`
	TeamID      string `json:"teamId,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	FileDir     string `json:"fileDir,omitempty"`
	TerminalDir string `json:"terminalDir,omitempty"`
}
type cloudWorkspace struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Entries     []cloudWorkspaceEntry `json:"entries"`
	Active      int                   `json:"active"`
	SplitFirst  int                   `json:"splitFirst"`
	SplitSecond int                   `json:"splitSecond"`
	SplitLayout string                `json:"splitLayout,omitempty"`
	ViewMode    string                `json:"viewMode,omitempty"`
	RowPerc     []float32             `json:"rowPercent,omitempty"`
	ColumnPerc  []float32             `json:"columnPercent,omitempty"`
}
type cloudWorkspaceSnapshot struct {
	Items    []cloudWorkspace `json:"items"`
	Revision string           `json:"revision"`
}

func portableWorkspace(w savedWorkspace, c *cloudClient, user string) (cloudWorkspace, error) {
	if c == nil || c.base == nil || validateSavedWorkspace(w) != nil {
		return cloudWorkspace{}, errors.New("工作区内容无效")
	}
	if !c.cloudWorkspaces16 && (len(w.Entries) > legacyWorkspaceSessions || len(w.RowPerc) > legacyWorkspaceSessions || len(w.ColumnPerc) > legacyWorkspaceSessions) {
		return cloudWorkspace{}, errors.New("同步服务需要升级才能保存超过 12 个终端的工作区")
	}
	out := cloudWorkspace{ID: w.ID, Name: w.Name, Active: w.Active, SplitFirst: w.SplitFirst, SplitSecond: w.SplitSecond, SplitLayout: w.SplitLayout, ViewMode: w.ViewMode, RowPerc: append([]float32(nil), w.RowPerc...), ColumnPerc: append([]float32(nil), w.ColumnPerc...)}
	for _, e := range w.Entries {
		if e.TabName != "" && !c.cloudWorkspaceNames {
			return cloudWorkspace{}, errors.New("同步服务需要升级才能保存工作区终端名称")
		}
		switch e.Kind {
		case "local":
			out.Entries = append(out.Entries, cloudWorkspaceEntry{Kind: "local", Label: e.Label, TabName: e.TabName})
		case "cloud":
			if e.CloudURL != c.base.String() || e.CloudUser != user {
				return cloudWorkspace{}, errors.New("工作区包含其他同步账号的主机")
			}
			out.Entries = append(out.Entries, cloudWorkspaceEntry{Kind: "cloud", Label: e.Label, TabName: e.TabName, Host: e.Host, Port: e.Port, User: e.User, TeamID: e.CloudTeam, AccountID: e.CloudID, FileDir: e.FileDir, TerminalDir: e.TerminalDir})
		default:
			return cloudWorkspace{}, errors.New("本地主机需先保存到云端才能同步工作区")
		}
	}
	return out, nil
}

func cloudWorkspaceLocal(w cloudWorkspace, c *cloudClient, user string, fingerprint func(cloudAccount) (string, error)) (savedWorkspace, error) {
	if c == nil || c.base == nil || user == "" {
		return savedWorkspace{}, errors.New("请先登录同步服务")
	}
	out := savedWorkspace{ID: w.ID, Name: w.Name, Active: w.Active, SplitFirst: w.SplitFirst, SplitSecond: w.SplitSecond, SplitLayout: w.SplitLayout, ViewMode: w.ViewMode, RowPerc: append([]float32(nil), w.RowPerc...), ColumnPerc: append([]float32(nil), w.ColumnPerc...)}
	for _, e := range w.Entries {
		switch e.Kind {
		case "local":
			if e.Host != "" || e.Port != "" || e.User != "" || e.TeamID != "" || e.AccountID != "" || e.FileDir != "" || e.TerminalDir != "" {
				return savedWorkspace{}, errors.New("云工作区内容无效")
			}
			out.Entries = append(out.Entries, workspaceTemplateEntry{Kind: "local", Label: e.Label, TabName: e.TabName})
		case "cloud":
			port, err := strconv.Atoi(e.Port)
			if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != e.Port || e.AccountID == "" || len(e.AccountID) > 128 || len(e.TeamID) > 128 || strings.ContainsAny(e.AccountID+e.TeamID, "/\\") {
				return savedWorkspace{}, errors.New("云主机引用无效")
			}
			fp, err := fingerprint(cloudAccount{Host: e.Host, Port: port, Username: e.User})
			if err != nil {
				return savedWorkspace{}, fmt.Errorf("%s：%w", e.Host, err)
			}
			out.Entries = append(out.Entries, workspaceTemplateEntry{Kind: "cloud", Label: e.Label, TabName: e.TabName, Host: e.Host, Port: e.Port, User: e.User, Fingerprint: fp, CloudURL: c.base.String(), CloudUser: user, CloudTeam: e.TeamID, CloudID: e.AccountID, FileDir: e.FileDir, TerminalDir: e.TerminalDir})
		default:
			return savedWorkspace{}, errors.New("云工作区包含不支持的会话")
		}
	}
	if err := validateSavedWorkspace(out); err != nil {
		return savedWorkspace{}, err
	}
	return out, nil
}

func validCloudWorkspaceSnapshot(s cloudWorkspaceSnapshot, c *cloudClient) bool {
	if len(s.Revision) != 64 || s.Items == nil || len(s.Items) > maxWorkspaceTemplates {
		return false
	}
	if _, err := hex.DecodeString(s.Revision); err != nil {
		return false
	}
	ids := map[string]bool{}
	for i, w := range s.Items {
		id, err := hex.DecodeString(w.ID)
		if err != nil || len(id) != 16 || hex.EncodeToString(id) != w.ID || ids[w.ID] {
			return false
		}
		ids[w.ID] = true
		for _, p := range s.Items[:i] {
			if strings.EqualFold(p.Name, w.Name) {
				return false
			}
		}
		if _, err := cloudWorkspaceLocal(w, c, "validation", func(cloudAccount) (string, error) { return "SHA256:validation", nil }); err != nil {
			return false
		}
	}
	return true
}
func (c *cloudClient) listCloudWorkspaces() (cloudWorkspaceSnapshot, error) {
	var s cloudWorkspaceSnapshot
	if !c.cloudWorkspaces || c.token == "" {
		return s, errors.New("当前服务不支持云工作区")
	}
	if err := c.request(http.MethodGet, "/api/v1/workspaces", nil, &s); err != nil {
		return s, err
	}
	if !validCloudWorkspaceSnapshot(s, c) {
		return cloudWorkspaceSnapshot{}, errors.New("云工作区响应无效")
	}
	return s, nil
}
func (c *cloudClient) replaceCloudWorkspaces(s cloudWorkspaceSnapshot) (cloudWorkspaceSnapshot, error) {
	var out cloudWorkspaceSnapshot
	if !c.cloudWorkspaces || c.token == "" || !validCloudWorkspaceSnapshot(s, c) {
		return out, errors.New("云工作区状态无效")
	}
	if !c.cloudWorkspaces16 {
		for _, w := range s.Items {
			if len(w.Entries) > legacyWorkspaceSessions || len(w.RowPerc) > legacyWorkspaceSessions || len(w.ColumnPerc) > legacyWorkspaceSessions {
				return out, errors.New("同步服务需要升级才能保存超过 12 个终端的工作区")
			}
		}
	}
	input := map[string]any{"items": s.Items, "expectedRevision": s.Revision}
	if c.cloudWorkspaceNames {
		input["supportsTabNames"] = true
	} else {
		for _, w := range s.Items {
			for _, entry := range w.Entries {
				if entry.TabName != "" {
					return out, errors.New("同步服务需要升级才能保存工作区终端名称")
				}
			}
		}
	}
	if err := c.request(http.MethodPut, "/api/v1/workspaces", input, &out); err != nil {
		return out, err
	}
	if !validCloudWorkspaceSnapshot(out, c) {
		return cloudWorkspaceSnapshot{}, errors.New("云工作区保存结果无效")
	}
	return out, nil
}
func (d *desktop) setCloudWorkspaceState(s cloudWorkspaceSnapshot, err error) {
	if d.cloud == nil || !d.cloud.cloudWorkspaces || err != nil || !validCloudWorkspaceSnapshot(s, d.cloud) {
		d.cloudWorkspaceSnapshot = cloudWorkspaceSnapshot{}
		d.cloudWorkspacesStale = true
		if err != nil {
			d.workspaceTemplatesStatus = "云工作区加载失败，请刷新：" + err.Error()
		}
		return
	}
	d.cloudWorkspaceSnapshot = s
	d.cloudWorkspacesStale = false
}
func (d *desktop) uploadWorkspaceTemplate(w savedWorkspace) {
	if d.busy || d.cloud == nil || !d.cloud.cloudWorkspaces || d.win == nil {
		return
	}
	if d.cloudWorkspacesStale {
		d.workspaceTemplatesStatus = "请先刷新云工作区"
		return
	}
	c := d.cloud
	portable, err := portableWorkspace(w, c, d.cloudUser)
	if err != nil {
		d.workspaceTemplatesStatus = "已保存到本机；" + err.Error()
		return
	}
	state := d.cloudWorkspaceSnapshot
	state.Items = append([]cloudWorkspace(nil), state.Items...)
	for _, existing := range state.Items {
		if existing.ID == portable.ID {
			a, _ := json.Marshal(existing)
			b, _ := json.Marshal(portable)
			if bytes.Equal(a, b) {
				d.workspaceTemplatesStatus = "工作区已同步"
				return
			}
		}
		if existing.ID == portable.ID || strings.EqualFold(existing.Name, portable.Name) {
			d.workspaceTemplatesStatus = "云端已有同名工作区，请先检查"
			return
		}
	}
	state.Items = append(state.Items, portable)
	d.busy = true
	d.workspaceTemplatesStatus = "正在同步工作区…"
	go func() {
		result, err := c.replaceCloudWorkspaces(state)
		d.win.Update(func() {
			d.busy = false
			if d.cloud != c {
				return
			}
			if err != nil {
				d.cloudWorkspacesStale = true
				d.workspaceTemplatesStatus = "同步未确认，请刷新后重试：" + err.Error()
				return
			}
			d.setCloudWorkspaceState(result, nil)
			d.workspaceTemplatesStatus = "工作区已同步"
		})
	}()
}

func (d *desktop) removeCloudWorkspace(id string) {
	if d.busy || d.cloud == nil || d.cloudWorkspacesStale || d.win == nil {
		return
	}
	if d.confirmCloudWorkspaceRemove != id {
		d.confirmCloudWorkspaceRemove = id
		return
	}
	c := d.cloud
	state := d.cloudWorkspaceSnapshot
	state.Items = make([]cloudWorkspace, 0, len(state.Items))
	for _, w := range d.cloudWorkspaceSnapshot.Items {
		if w.ID != id {
			state.Items = append(state.Items, w)
		}
	}
	d.busy = true
	go func() {
		result, err := c.replaceCloudWorkspaces(state)
		d.win.Update(func() {
			d.busy = false
			if d.cloud != c {
				return
			}
			d.confirmCloudWorkspaceRemove = ""
			if err != nil {
				d.cloudWorkspacesStale = true
				d.workspaceTemplatesStatus = "删除未确认，请刷新后重试：" + err.Error()
				return
			}
			d.setCloudWorkspaceState(result, nil)
			d.workspaceTemplatesStatus = "云工作区已删除"
		})
	}()
}
