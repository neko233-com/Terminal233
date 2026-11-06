package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// A modifier range uses the currently displayed ordering. Ctrl preserves
// existing selections, including ones hidden by a search or filter.
func profileModifiedSelection[T comparable](previous map[T]bool, anchor, clicked T, visible []T, mods ui.Modifiers) (map[T]bool, T) {
	index, anchorIndex := -1, -1
	for i, id := range visible {
		if id == clicked {
			index = i
		}
		if id == anchor {
			anchorIndex = i
		}
	}
	if index < 0 {
		return previous, anchor
	}
	selected := make(map[T]bool)
	if mods&ui.Cmd != 0 {
		for id, yes := range previous {
			if yes {
				selected[id] = true
			}
		}
	}
	if mods&ui.Shift != 0 && anchorIndex >= 0 {
		lo, hi := anchorIndex, index
		if lo > hi {
			lo, hi = hi, lo
		}
		for _, id := range visible[lo : hi+1] {
			selected[id] = true
		}
		return selected, anchor
	}
	if mods&ui.Cmd != 0 && len(selected) == 0 && anchorIndex >= 0 && anchor != clicked {
		selected[anchor] = true
	}
	if mods&ui.Cmd != 0 && selected[clicked] {
		delete(selected, clicked)
		if len(selected) == 0 {
			var empty T
			return selected, empty
		}
	} else {
		selected[clicked] = true
	}
	return selected, clicked
}

func (d *desktop) selectLocalProfilesModified(id profileID, visible []profileID, mods ui.Modifiers) {
	d.checkedProfiles, d.profileSelectionAnchor = profileModifiedSelection(d.checkedProfiles, d.profileSelectionAnchor, id, visible, mods)
	d.selectedProfile, d.selectedCloudProfile = "", ""
	d.confirmProfileBatchDelete = false
	d.showProfileInspector = false
	d.profileInspectorAt = time.Time{}
}
func (d *desktop) selectCloudProfilesModified(id string, visible []string, mods ui.Modifiers) {
	previous := make(map[string]bool, len(d.checkedCloud))
	for key := range d.checkedCloud {
		previous[key] = true
	}
	selected, anchor := profileModifiedSelection(previous, d.cloudSelectionAnchor, id, visible, mods)
	accounts := make(map[string]cloudAccount, len(d.accounts))
	for _, a := range d.accounts {
		accounts[a.ID] = a
	}
	next := make(map[string]cloudAccount, len(selected))
	for key := range selected {
		if a, ok := accounts[key]; ok {
			next[key] = a
		} else if old, ok := d.checkedCloud[key]; ok {
			next[key] = old
		}
	}
	d.checkedCloud, d.cloudSelectionAnchor = next, anchor
	d.selectedCloudProfile, d.selectedProfile = "", ""
	d.confirmCloudBatchDelete = false
	d.showProfileInspector = false
	d.profileInspectorAt = time.Time{}
}
