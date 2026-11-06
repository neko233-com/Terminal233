package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const connectionInspectorWidth float32 = 360

type connectionDraft [32]byte

func (d *desktop) currentConnectionDraft() connectionDraft {
	h := sha256.New()
	var size [8]byte
	for _, value := range []string{d.settings.Host, d.settings.Port, d.settings.User, d.settings.Password, d.settings.KeyPath, d.settings.Passphrase, d.profileLabel, d.profileGroup} {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(value))
	}
	var result connectionDraft
	copy(result[:], h.Sum(nil))
	return result
}

func (d *desktop) captureConnectionDraft() {
	d.connectionDraft = d.currentConnectionDraft()
	d.connectionDraftSet = true
	d.confirmDiscardConnection = false
}

func (d *desktop) closeConnectionEditor() bool {
	if d.busy {
		return false
	}
	current := d.currentConnectionDraft()
	if d.connectionDraftSet && current != d.connectionDraft && !d.confirmDiscardConnection {
		d.confirmDiscardConnection = true
		d.status = "主机详情有未保存的修改；再次点击返回可放弃修改。"
		return false
	}
	d.editConnection = false
	d.connectionDraft, d.connectionDraftSet, d.confirmDiscardConnection = connectionDraft{}, false, false
	d.settings.Password, d.settings.Passphrase = "", ""
	return true
}

// Keep enough space for both the host grid and the inspector. A narrow
// window uses the existing full-width editor instead.
func (d *desktop) connectionInspectorDocked(windowWidth float32) bool {
	if !d.sidebarCollapsed {
		windowWidth -= workspaceSidebarWidth(windowWidth)
	}
	return windowWidth >= 820
}

// Delay opening the inspector for the double-click interval. Otherwise its
// layout shift can move a host between the first and second click.
func (d *desktop) scheduleProfileInspector(c *ui.Context) {
	if d.showProfileInspector {
		return
	}
	d.profileInspectorAt = time.Now().Add(350 * time.Millisecond)
	c.After(350 * time.Millisecond)
}

func (d *desktop) inspectedProfile() *profile {
	if !d.showProfileInspector || d.selectedProfile == "" {
		return nil
	}
	for i := range d.profiles {
		p := &d.profiles[i]
		if string(p.ID) == d.selectedProfile && d.profileVisible(*p) {
			return p
		}
	}
	return nil
}

func (d *desktop) inspectedCloudAccount() *cloudAccount {
	if !d.showProfileInspector || d.cloud == nil || d.selectedCloudProfile == "" || d.profileSource == "local" || d.profileRecent || d.profileFavorites {
		return nil
	}
	for i := range d.accounts {
		account := &d.accounts[i]
		if account.ID == d.selectedCloudProfile && d.cloudAccountVisible(*account) {
			return account
		}
	}
	return nil
}

func (d *desktop) inspectedProfileAvailable() bool {
	return d.inspectedProfile() != nil || d.inspectedCloudAccount() != nil
}

func (d *desktop) profileInspectorView(c *ui.Context) {
	local, cloud := d.inspectedProfile(), d.inspectedCloudAccount()
	if local == nil && cloud == nil {
		return
	}
	name, address, user, group, source := "", "", "", "", "本机"
	if local != nil {
		name, user, group = local.Label, local.Settings.User, local.Group
		address = net.JoinHostPort(local.Settings.Host, local.Settings.Port)
	} else {
		name, user, source = cloud.Label, cloud.Username, d.cloudScopeName()
		group = d.cloudGroupSnapshot.Assignments[cloud.ID]
		address = net.JoinHostPort(cloud.Host, fmt.Sprint(cloud.Port))
	}
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Text(c, "主机详情").FontSize(17).Bold().Grow(1)
		if ui.Button(c.Key("inspector-close"), "×").Label("关闭主机详情").Tooltip("关闭").Width(36).Clicked() {
			d.showProfileInspector = false
			d.profileInspectorAt = time.Time{}
		}
	})
	ui.Text(c, source).FontSize(12).TextColor(c.Theme().TextMuted)
	ui.Column(c).FillWidth().Padding(14).Gap(6).Background(c.Theme().Background).Radius(8).Children(func() {
		ui.Text(c, name).FontSize(16).Bold().Wrap()
		ui.Text(c, user+" · SSH").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		ui.Text(c, address).FontSize(13).Wrap()
		if group != "" {
			ui.Text(c, "分组："+group).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		}
	})
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if ui.PrimaryButton(c.Key("inspector-connect"), "连接").Disabled(d.busy || len(d.sessions) >= maxWorkspaceSessions).Clicked() {
			if local != nil {
				d.openProfileConnection(*local)
			} else {
				d.loadCloudAccount(*cloud)
			}
		}
		if ui.Button(c.Key("inspector-edit"), "编辑主机").Disabled(d.busy).Clicked() {
			if local != nil {
				d.loadProfile(*local)
			} else {
				d.loadCloudAccount(*cloud)
			}
		}
		if ui.Button(c.Key("inspector-copy"), "复制地址").Clicked() {
			mygo.Clipboard.WriteText(address)
			d.status = "已复制地址：" + name
		}
		if local != nil {
			label := "收藏"
			if local.Favorite {
				label = "取消收藏"
			}
			if ui.Button(c.Key("inspector-favorite"), label).Disabled(d.busy).Clicked() {
				d.toggleProfileFavorite(local.ID)
			}
		}
	})
}
