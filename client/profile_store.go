package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

func (d *desktop) loadProfiles(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		d.profilePath = path
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
	if saved.Version != 1 || len(saved.Items) > 10000 {
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
	return nil
}

func (d *desktop) saveProfiles() error {
	if d.profileStoreBlocked {
		return errors.New("连接存储不可用，原文件未覆盖")
	}
	if d.profilePath == "" {
		return nil
	}
	saved := diskProfiles{Version: 1, Items: make([]diskProfile, 0, len(d.profiles))}
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
	return os.Rename(f.Name(), d.profilePath)
}
