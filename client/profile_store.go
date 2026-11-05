package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// diskProfile is deliberately a whitelist: SSH passwords and private-key
// passphrases can never be serialized through this type.
type diskProfile struct {
	Group, Label, Host, Port, User, KeyPath, Fingerprint string
	Favorite                                             bool  `json:",omitempty"`
	LastConnected                                        int64 `json:",omitempty"`
}

type diskProfiles struct {
	Version int
	Items   []diskProfile
	Groups  []string `json:",omitempty"`
}

func (d *desktop) loadProfiles(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		d.confirmProfileDelete = ""
		d.checkedProfiles, d.confirmProfileBatchDelete = nil, false
		d.profiles, d.nextProfileID = nil, 0
		d.profileGroups, d.confirmGroupDelete = nil, false
		d.profileFormat, d.foldedGroups = 1, nil
		d.profilePath = path
		d.profileExists = false
		d.profileRevision = [32]byte{}
		d.profileStoreConflict = false
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("连接列表文件过大")
	}
	var saved diskProfiles
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("连接列表格式无效：%w", err)
	}
	if (saved.Version != 1 && saved.Version != 2) || len(saved.Items) > 10000 || len(saved.Groups) > 10000 {
		return errors.New("连接列表版本或数量无效")
	}
	profiles := make([]profile, 0, len(saved.Items))
	for i, p := range saved.Items {
		settings := connectionSettings{Host: p.Host, Port: p.Port, User: p.User, KeyPath: p.KeyPath, Fingerprint: p.Fingerprint}
		if _, err := settings.address(); err != nil {
			return fmt.Errorf("连接列表第 %d 项无效：%w", i+1, err)
		}
		profiles = append(profiles, profile{ID: profileID(fmt.Sprint(i + 1)), Group: p.Group, Label: p.Label, Favorite: p.Favorite, LastConnected: p.LastConnected, Settings: settings})
	}
	d.profiles, d.nextProfileID, d.profilePath = profiles, len(profiles), path
	d.profileGroups, d.confirmGroupDelete = saved.Groups, false
	d.profileFormat, d.foldedGroups = saved.Version, nil
	d.confirmProfileDelete = ""
	d.checkedProfiles, d.confirmProfileBatchDelete = nil, false
	d.profileExists, d.profileRevision = true, sha256.Sum256(data)
	d.profileStoreConflict = false
	return nil
}

func (d *desktop) saveProfiles() error {
	if d.profileStoreBlocked {
		return errors.New("连接存储不可用，原文件未覆盖")
	}
	if d.profilePath == "" {
		return nil
	}
	version := 1
	if len(d.profileGroups) != 0 || d.profileFormat == 2 {
		version = 2
	}
	saved := diskProfiles{Version: version, Items: make([]diskProfile, 0, len(d.profiles)), Groups: d.profileGroups}
	for _, p := range d.profiles {
		s := p.Settings
		saved.Items = append(saved.Items, diskProfile{Group: p.Group, Label: p.Label, Favorite: p.Favorite, LastConnected: p.LastConnected, Host: s.Host, Port: s.Port, User: s.User, KeyPath: s.KeyPath, Fingerprint: s.Fingerprint})
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(d.profilePath)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Serialize cooperating writers and reject stale snapshots. Without both
	// checks, two windows can silently erase each other's connection edits.
	lock := flock.New(d.profilePath + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("另一个窗口正在保存连接，请稍后重试")
	}
	defer lock.Unlock()
	current, err := os.ReadFile(d.profilePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exists := err == nil
	if exists != d.profileExists || (exists && sha256.Sum256(current) != d.profileRevision) {
		d.profileStoreConflict = true
		return errors.New("连接文件已被其他窗口更改，当前更改尚未保存；可返回连接列表重新载入")
	}
	f, err := os.CreateTemp(dir, ".profiles-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), d.profilePath); err != nil {
		return err
	}
	d.profileExists, d.profileRevision = true, sha256.Sum256(data)
	d.profileFormat = version
	d.profileStoreConflict = false
	return nil
}
