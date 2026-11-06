package main

import "reflect"

// Capture the selection and source space before MyGo starts the drag. The
// value is immutable for its lifetime and is revalidated before any action.
type localHostDrag struct {
	ID       profileID
	Items    []profile
	Selected bool
}

type cloudHostDrag struct {
	ID       cloudGroupDragID
	Items    []cloudAccount
	Selected bool
	Client   *cloudClient
	Team     string
	User     string
	URL      string
}

func (localHostDrag) NekoFreezeDragValue()    {}
func (cloudHostDrag) NekoFreezeDragValue()    {}
func (quickSnippetDrag) NekoFreezeDragValue() {}

func (d *desktop) selectedLocalHostDragItems() []profile {
	var items []profile
	count := 0
	for _, checked := range d.checkedProfiles {
		if checked {
			count++
		}
	}
	for _, item := range d.profiles {
		if d.checkedProfiles[item.ID] {
			if item.Tags != nil {
				item.Tags = append([]string{}, item.Tags...)
			}
			items = append(items, item)
		}
	}
	if len(items) != count {
		return nil
	}
	return items
}

func (d *desktop) localHostDragFor(p profile, selected []profile) localHostDrag {
	drag := localHostDrag{ID: p.ID, Selected: d.checkedProfiles[p.ID]}
	if drag.Selected {
		drag.Items = selected
	} else {
		if p.Tags != nil {
			p.Tags = append([]string{}, p.Tags...)
		}
		drag.Items = []profile{p}
	}
	return drag
}

func (d *desktop) selectedCloudHostDragItems() []cloudAccount {
	var items []cloudAccount
	for _, item := range d.accounts {
		if _, checked := d.checkedCloud[item.ID]; checked {
			if item.Tags != nil {
				item.Tags = append([]string{}, item.Tags...)
			}
			items = append(items, item)
		}
	}
	if len(items) != len(d.checkedCloud) {
		return nil
	}
	return items
}

func (d *desktop) cloudHostDragFor(account cloudAccount, selected []cloudAccount) cloudHostDrag {
	drag := cloudHostDrag{ID: cloudGroupDragID(account.ID), Client: d.cloud, User: d.cloudUser}
	if d.cloud != nil {
		drag.Team = d.cloud.teamID
		if d.cloud.base != nil {
			drag.URL = d.cloud.base.String()
		}
	}
	_, drag.Selected = d.checkedCloud[account.ID]
	if drag.Selected {
		drag.Items = selected
	} else {
		if account.Tags != nil {
			account.Tags = append([]string{}, account.Tags...)
		}
		drag.Items = []cloudAccount{account}
	}
	return drag
}

func (d *desktop) validLocalHostDrag(drag localHostDrag) bool {
	if drag.ID == "" || len(drag.Items) == 0 {
		return false
	}
	for _, expected := range drag.Items {
		current, ok := d.currentProfile(expected.ID)
		if !ok || !reflect.DeepEqual(current, expected) {
			return false
		}
	}
	return true
}

func (d *desktop) validCloudHostDrag(drag cloudHostDrag) bool {
	if drag.ID == "" || len(drag.Items) == 0 || drag.Client == nil || d.cloud != drag.Client || d.cloud.base == nil || d.cloud.base.String() != drag.URL || d.cloud.teamID != drag.Team || d.cloudUser != drag.User {
		return false
	}
	for _, expected := range drag.Items {
		found := false
		for _, account := range d.accounts {
			if account.ID == expected.ID && reflect.DeepEqual(account, expected) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (d *desktop) moveLocalHostDrag(drag localHostDrag, group string) {
	if !d.validLocalHostDrag(drag) {
		d.status = "主机或选择已改变，请重新拖拽"
		return
	}
	ids := make([]profileID, 0, len(drag.Items))
	for _, item := range drag.Items {
		ids = append(ids, item.ID)
	}
	// Preserve any selection made after the drag began.
	clear := drag.Selected && len(d.checkedProfiles) == len(ids)
	for _, id := range ids {
		clear = clear && d.checkedProfiles[id]
	}
	d.moveProfilesToGroup(ids, group, clear)
}

func (d *desktop) moveCloudHostDrag(drag cloudHostDrag, group string) {
	if !d.validCloudHostDrag(drag) {
		d.status = "云空间或主机已改变，请重新拖拽"
		return
	}
	ids := make([]string, 0, len(drag.Items))
	clear := drag.Selected && len(d.checkedCloud) == len(drag.Items)
	for _, item := range drag.Items {
		ids = append(ids, item.ID)
		_, selected := d.checkedCloud[item.ID]
		clear = clear && selected
	}
	d.cloudGroupOperationForIDs("move", ids, group, clear)
}
