package main

import (
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/egoist/mygo/ui"
)

const connectionInspectorWidth float32 = 360

type connectionDraft struct {
	metadata [32]byte
	secrets  [32]byte
}

func connectionDraftHash(values ...string) [32]byte {
	h := sha256.New()
	var size [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(value))
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func (d *desktop) currentConnectionDraft() connectionDraft {
	return connectionDraft{
		metadata: connectionDraftHash(d.settings.Host, d.settings.Port, d.settings.User, d.settings.KeyPath, d.profileLabel, d.profileGroup, d.profileTags),
		secrets:  connectionDraftHash(d.settings.Password, d.settings.Passphrase),
	}
}

func (d *desktop) captureConnectionDraft() {
	d.connectionDraft = d.currentConnectionDraft()
	d.connectionDraftSet = true
	d.confirmDiscardConnection = false
	d.profileAutoDirty, d.profileAutoAt = false, time.Time{}
}

func (d *desktop) hasUnsavedConnection() bool {
	return d.editConnection && d.connectionDraftSet && d.currentConnectionDraft() != d.connectionDraft
}

// A complete local draft can be committed when the user moves elsewhere,
// without waiting for the debounce timer. Incomplete and cloud drafts still
// require an explicit decision, and failed writes never discard the form.
func (d *desktop) connectionDraftCanAutosave() bool {
	if !d.editConnection || !d.connectionDraftSet || d.settingsFromCloud || d.profilePath == "" || d.busy || d.profileStoreConflict || d.profileStoreBlocked || d.profileSecretError || (!d.profileAutoDirty && d.profileAutoAt.IsZero()) {
		return false
	}
	_, err := d.settings.address()
	return err == nil
}

func (d *desktop) connectionNavigationBlocked() bool {
	return d.hasUnsavedConnection() && !d.connectionDraftCanAutosave()
}

func (d *desktop) prepareConnectionNavigation() bool {
	attemptedSave := d.connectionDraftCanAutosave()
	if attemptedSave {
		d.flushProfileAutosave()
	}
	if d.hasUnsavedConnection() {
		if !attemptedSave {
			d.status = "请先保存或关闭当前主机详情。"
		}
		return false
	}
	return true
}

func (d *desktop) closeConnectionForNavigation() bool {
	if !d.prepareConnectionNavigation() {
		return false
	}
	return !d.editConnection || d.closeConnectionEditor()
}

func (d *desktop) closeConnectionEditor() bool {
	if d.busy {
		return false
	}
	if d.profileAutoDirty || !d.profileAutoAt.IsZero() {
		d.flushProfileAutosave()
	}
	if d.hasUnsavedConnection() && !d.confirmDiscardConnection {
		d.confirmDiscardConnection = true
		d.status = "主机详情有未保存的修改；再次点击返回可放弃修改。"
		return false
	}
	d.editConnection = false
	d.connectionFocusPending = false
	d.showKeyOptions = false
	d.showConnectionTools = false
	d.fileReturnAfterConnect = false
	d.clearSecondaryConnectIntent()
	d.connectionDraft, d.connectionDraftSet, d.confirmDiscardConnection = connectionDraft{}, false, false
	d.profileAutoDirty, d.profileAutoAt = false, time.Time{}
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
	delay := hostInspectorDelay()
	d.profileInspectorAt = time.Now().Add(delay)
	c.After(delay)
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
