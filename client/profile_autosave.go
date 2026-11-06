package main

import (
	"errors"
	"time"

	"github.com/egoist/mygo/ui"
)

const profileAutosaveDelay = 800 * time.Millisecond

// saveCurrentProfile rolls back only the temporary list changes if writing
// fails. The form and its credentials remain available for the user to fix.
func (d *desktop) saveCurrentProfile() error {
	if d.selectedProfile != "" {
		found := false
		for _, p := range d.profiles {
			if string(p.ID) == d.selectedProfile {
				found = true
				break
			}
		}
		if !found {
			return errors.New("原连接已不存在，请重新载入")
		}
	}
	before := append([]profile(nil), d.profiles...)
	selected, nextID := d.selectedProfile, d.nextProfileID
	if err := d.addProfile(); err != nil {
		return err
	}
	if err := d.saveProfiles(); err != nil {
		d.profiles, d.selectedProfile, d.nextProfileID = before, selected, nextID
		return err
	}
	return nil
}

// Only local non-secret metadata is eligible. Cloud writes still use their
// explicit permission, fingerprint and concurrency checks.
func (d *desktop) flushProfileAutosave() {
	d.profileAutoDirty, d.profileAutoAt = false, time.Time{}
	if !d.editConnection || d.settingsFromCloud || !d.connectionDraftSet || d.profilePath == "" || d.profileStoreConflict || d.busy {
		return
	}
	current := d.currentConnectionDraft()
	if current.metadata == d.connectionDraft.metadata && d.selectedProfile != "" {
		return
	}
	if _, err := d.settings.address(); err != nil {
		return // An incomplete form remains an unsaved draft, not a saved host.
	}
	if err := d.saveCurrentProfile(); err != nil {
		d.status = "自动保存失败，表单内容仍保留：" + err.Error()
		return
	}
	// Passwords and private-key passphrases are never persisted locally, so
	// keep their prior baseline and continue protecting unsaved secret edits.
	d.connectionDraft.metadata = current.metadata
	d.confirmDiscardConnection = false
	d.status = "已自动保存"
}

func (d *desktop) serviceProfileAutosave(c *ui.Context) {
	if !d.editConnection || d.settingsFromCloud || !d.connectionDraftSet || d.profilePath == "" {
		d.profileAutoDirty, d.profileAutoAt = false, time.Time{}
		return
	}
	if d.profileAutoDirty {
		d.profileAutoDirty = false
		d.profileAutoAt = c.Now().Add(profileAutosaveDelay)
	}
	if d.profileAutoAt.IsZero() {
		return
	}
	remaining := d.profileAutoAt.Sub(c.Now())
	if remaining > 0 {
		c.After(remaining)
		return
	}
	if d.busy {
		c.After(200 * time.Millisecond)
		return
	}
	d.flushProfileAutosave()
}
