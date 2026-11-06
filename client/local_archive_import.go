package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

func newUnusedBackupID(used map[string]bool) (string, error) {
	for n := 0; n < 5; n++ {
		id, err := newRandomID()
		if err != nil {
			return "", err
		}
		if !used[id] {
			used[id] = true
			return id, nil
		}
	}
	return "", errors.New("无法生成唯一标识，请重试")
}

// Imports both stores using a recoverable journal under their respective locks.
func (d *desktop) importLocalArchive(doc localArchiveDocument) error {
	return d.importLocalArchiveWith(doc, nil)
}

func (d *desktop) importLocalArchiveWith(doc localArchiveDocument, fault func(string) error) error {
	if err := validateLocalArchive(doc); err != nil {
		return err
	}
	if d.profilePath == "" || d.forwardRulesPath == "" || d.profileStoreBlocked {
		return errors.New("本机存储不可用")
	}
	if len(d.profiles)+len(doc.Profiles) > 10000 || len(d.identities)+len(doc.Identities) > maxSavedIdentities {
		return errors.New("导入后主机或身份数量超过上限")
	}
	beforeProfiles, beforeIdentities, beforeGroups, beforeNext := d.profiles, d.identities, d.profileGroups, d.nextProfileID
	profiles := append([]profile(nil), d.profiles...)
	identities := append([]savedIdentity(nil), d.identities...)
	groups := append([]string(nil), d.profileGroups...)
	usedIdentity := map[string]bool{}
	for _, i := range identities {
		usedIdentity[i.ID] = true
	}
	usedHost := map[string]bool{}
	for _, p := range profiles {
		if p.StableID != "" {
			usedHost[p.StableID] = true
		}
	}
	identityMap := map[string]string{}
	hostMap := map[string]string{}
	for _, src := range doc.Identities {
		id, err := newUnusedBackupID(usedIdentity)
		if err != nil {
			return err
		}
		name, err := importedIdentityName(src.Name, identities)
		if err != nil {
			return err
		}
		i := savedIdentity{ID: id, Name: name, User: src.User, KeyPath: src.KeyPath}
		s := identitySecretSettings(i)
		s.Password, s.Passphrase = src.Password, src.Passphrase
		i.EncryptedCredentials, err = protectProfileCredentials(s)
		if err != nil {
			return err
		}
		identityMap[src.ID] = id
		identities = append(identities, i)
	}
	next := d.nextProfileID
	for _, src := range doc.Profiles {
		next++
		p := profile{ID: profileID(fmt.Sprint(next)), Group: src.Group, Label: src.Label, Tags: append([]string(nil), src.Tags...), Favorite: src.Favorite, Settings: connectionSettings{Host: src.Host, Port: src.Port, User: src.User, KeyPath: src.KeyPath}}
		if src.StableID != "" {
			id, err := newUnusedBackupID(usedHost)
			if err != nil {
				return err
			}
			p.StableID = id
			hostMap[src.StableID] = id
		}
		if src.IdentityID != "" {
			p.IdentityID = identityMap[src.IdentityID]
			binding, err := sealIdentityBinding(p.Settings, p.IdentityID)
			if err != nil {
				return err
			}
			p.IdentityBinding = binding
		} else {
			s := p.Settings
			s.Password, s.Passphrase = src.Password, src.Passphrase
			protected, err := protectProfileCredentials(s)
			if err != nil {
				return err
			}
			p.EncryptedCredentials = protected
		}
		profiles = append(profiles, p)
	}
	groupSet := map[string]bool{}
	for _, g := range groups {
		groupSet[g] = true
	}
	for _, g := range doc.Groups {
		if !groupSet[g] {
			groups = append(groups, g)
			groupSet[g] = true
		}
	}
	if len(groups) > 10000 {
		return errors.New("导入后分组数量超过上限")
	}
	unlock, err := lockLocalImport(d.profilePath, d.forwardRulesPath)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkLocalImportPending(d.profilePath); err != nil {
		return err
	}
	oldProfiles, profilesExist, err := readLocalStore(d.profilePath, 4<<20)
	if err != nil {
		return err
	}
	if profilesExist != d.profileExists || (profilesExist && sha256.Sum256(oldProfiles) != d.profileRevision) {
		d.profileStoreConflict = true
		return errors.New("本机连接已被其他窗口修改，请重新载入")
	}
	oldRules, rulesExist, err := readLocalStore(d.forwardRulesPath, 2<<20)
	if err != nil {
		return err
	}
	file, err := readForwardRulesUnchecked(d.forwardRulesPath)
	if err != nil {
		return err
	}
	if len(file.Items)+len(doc.Rules) > maxSavedForwardRules {
		return errors.New("导入后转发规则数量超过上限")
	}
	usedRules := map[string]bool{}
	for _, r := range file.Items {
		usedRules[r.ID] = true
	}
	for _, src := range doc.Rules {
		id, e := newUnusedBackupID(usedRules)
		if e != nil {
			return e
		}
		hostID, ok := hostMap[src.HostID]
		if !ok {
			hostID, e = newUnusedBackupID(usedHost)
			if e != nil {
				return e
			}
			hostMap[src.HostID] = hostID
		}
		src.ID, src.HostID = id, hostID
		file.Items = append(file.Items, src)
	}
	d.profiles, d.identities, d.profileGroups, d.nextProfileID = profiles, identities, groups, next
	newProfiles, version, err := d.encodeProfiles()
	d.profiles, d.identities, d.profileGroups, d.nextProfileID = beforeProfiles, beforeIdentities, beforeGroups, beforeNext
	if err != nil {
		return err
	}
	newRules, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err = commitLocalImportLocked(d.profilePath, d.forwardRulesPath, oldProfiles, profilesExist, oldRules, rulesExist, newProfiles, newRules, fault); err != nil {
		if checkLocalImportPending(d.profilePath) != nil {
			d.profileStoreBlocked = true
		}
		return err
	}
	d.profiles, d.identities, d.profileGroups, d.nextProfileID = profiles, identities, groups, next
	d.profileExists, d.profileRevision, d.profileFormat = true, sha256.Sum256(newProfiles), version
	d.profileStoreConflict = false
	d.forwardRules = file.Items
	d.stopChangedForwardRules()
	d.reconcileForwardRules()
	return nil
}
