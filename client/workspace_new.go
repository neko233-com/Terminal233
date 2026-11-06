package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

func workspaceCloudSelectionKey(client *cloudClient, user, id string) string {
	if client == nil || client.base == nil {
		return ""
	}
	return fmt.Sprintf("cloud:%q:%q:%q:%q", client.base.String(), user, client.teamID, id)
}

// A cloud-provided fingerprint is not local trust. Only a previously verified
// local profile or live session can establish it for a new workspace.
func (d *desktop) trustedWorkspaceCloudFingerprint(a cloudAccount) (string, error) {
	port := strconv.Itoa(a.Port)
	trusted := ""
	accept := func(host, p, user, fingerprint string) error {
		if host != a.Host || p != port || user != a.Username || !strings.HasPrefix(fingerprint, "SHA256:") {
			return nil
		}
		if trusted != "" && trusted != fingerprint {
			return errors.New("本机保存了不同的主机指纹，请先检查主机")
		}
		trusted = fingerprint
		return nil
	}
	for _, p := range d.profiles {
		if err := accept(p.Settings.Host, p.Settings.Port, p.Settings.User, p.Settings.Fingerprint); err != nil {
			return "", err
		}
	}
	for _, s := range d.sessions {
		if s.shell != nil {
			if err := accept(s.active.Host, s.active.Port, s.active.User, s.active.Fingerprint); err != nil {
				return "", err
			}
		}
	}
	if trusted == "" {
		return "", errors.New("请先打开该云主机并确认主机指纹")
	}
	return trusted, nil
}

func (d *desktop) workspaceFromSelection() (savedWorkspace, error) {
	name := strings.TrimSpace(d.workspaceTemplateName)
	if name == "" {
		name = "工作区"
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return savedWorkspace{}, err
	}
	w := savedWorkspace{ID: hex.EncodeToString(id[:]), Name: name, Active: 0, SplitFirst: -1, SplitSecond: -1, ViewMode: "focus"}
	selected := 0
	for _, value := range d.workspaceHostSelected {
		if value {
			selected++
		}
	}
	if d.workspaceHostSelected["local"] {
		w.Entries = append(w.Entries, workspaceTemplateEntry{Kind: "local", Label: "本机终端"})
	}
	for _, p := range d.profiles {
		if !d.workspaceHostSelected["profile:"+string(p.ID)] {
			continue
		}
		s, err := d.profileCredential(p)
		if err != nil {
			return savedWorkspace{}, fmt.Errorf("%s：%w", p.Label, err)
		}
		s, err = d.resolveSavedRoute(s)
		if err != nil {
			return savedWorkspace{}, fmt.Errorf("%s：%w", p.Label, err)
		}
		w.Entries = append(w.Entries, workspaceTemplateEntry{Kind: "profile", Label: p.Label, Group: p.Group, Host: s.Host, Port: s.Port, User: s.User, KeyPath: s.KeyPath, Fingerprint: s.Fingerprint, JumpRoute: s.savedRouteSignature()})
	}
	if d.cloud != nil && d.cloud.base != nil {
		for _, a := range d.accounts {
			key := workspaceCloudSelectionKey(d.cloud, d.cloudUser, a.ID)
			if !d.workspaceHostSelected[key] {
				continue
			}
			if d.cloudListStale || (d.cloud.teamID != "" && d.teamListStale) {
				return savedWorkspace{}, errors.New("请先刷新云主机列表")
			}
			fingerprint, err := d.trustedWorkspaceCloudFingerprint(a)
			if err != nil {
				return savedWorkspace{}, fmt.Errorf("%s：%w", a.Label, err)
			}
			e := workspaceTemplateEntry{Kind: "cloud", Host: a.Host, Port: strconv.Itoa(a.Port), User: a.Username, Fingerprint: fingerprint, CloudURL: d.cloud.base.String(), CloudUser: d.cloudUser, CloudTeam: d.cloud.teamID, CloudID: a.ID}
			if id := cloudJumpReference(a); id != "" {
				settings := connectionSettings{Host: e.Host, Port: e.Port, User: e.User, Fingerprint: fingerprint, cloudJumpID: id, cloudRouteURL: e.CloudURL, cloudRouteUser: e.CloudUser, cloudRouteTeam: e.CloudTeam}
				resolved, err := d.resolveCloudRoute(settings)
				if err != nil {
					return savedWorkspace{}, err
				}
				e.JumpRoute = resolved.savedRouteSignature()
			}
			w.Entries = append(w.Entries, e)
		}
	}
	if selected == 0 || selected != len(w.Entries) {
		return savedWorkspace{}, errors.New("请选择可用的主机或本机终端")
	}
	if err := validateSavedWorkspace(w); err != nil {
		return savedWorkspace{}, fmt.Errorf("无法创建工作区：%w；请先连接并确认主机", err)
	}
	return w, nil
}

func (d *desktop) workspaceHostSelectionControls(c *ui.Context) {
	label := "从主机创建工作区"
	if d.showWorkspaceHostSelection {
		label = "取消"
	}
	if ui.Button(c.Key("workspace-hosts-toggle"), label).Disabled(d.busy).Clicked() {
		d.showWorkspaceHostSelection = !d.showWorkspaceHostSelection
		d.showWorkspaceTemplates = false
		d.workspaceHostSelected = nil
		d.workspaceTemplatesStatus = ""
	}
	if d.showWorkspaceHostSelection {
		if ui.PrimaryButton(c.Key("workspace-hosts-open"), "打开工作区").Disabled(d.busy || len(d.workspaceHostSelected) == 0).Clicked() {
			w, err := d.workspaceFromSelection()
			if err != nil {
				d.workspaceTemplatesStatus = err.Error()
			} else {
				d.startWorkspace(w, false)
			}
		}
	}
}

func (d *desktop) workspaceResultKey(r newTabResult) string {
	switch r.kind {
	case "local":
		return "local"
	case "profile":
		return "profile:" + r.id
	case "cloud":
		return workspaceCloudSelectionKey(d.cloud, d.cloudUser, r.id)
	}
	return ""
}

func (d *desktop) toggleWorkspaceResult(r newTabResult) {
	key := d.workspaceResultKey(r)
	if key == "" {
		return
	}
	if d.workspaceHostSelected[key] {
		delete(d.workspaceHostSelected, key)
	} else if d.sessionCount(false)+len(d.workspaceHostSelected) >= maxWorkspaceSessions {
		d.workspaceTemplatesStatus = fmt.Sprintf("最多同时打开 %d 个终端", maxWorkspaceSessions)
	} else {
		if d.workspaceHostSelected == nil {
			d.workspaceHostSelected = make(map[string]bool)
		}
		d.workspaceHostSelected[key] = true
		d.workspaceTemplatesStatus = ""
	}
}
