package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const maxSavedIdentities = 1000

// Only OS-protected ciphertext contains the password or private-key phrase.
type savedIdentity struct {
	ID, Name, User, KeyPath string
	EncryptedCredentials    string `json:",omitempty"`
}
type identityDraft struct{ Name, User, KeyPath, Password, Passphrase string }

func validateSavedIdentity(i savedIdentity) error {
	id, err := hex.DecodeString(i.ID)
	if err != nil || len(id) != 16 || hex.EncodeToString(id) != i.ID || strings.TrimSpace(i.Name) != i.Name || i.Name == "" || !validWorkspaceText(i.Name, 120) || strings.TrimSpace(i.User) != i.User || i.User == "" || !validWorkspaceText(i.User, 256) || !validWorkspaceText(i.KeyPath, 4096) || len(i.EncryptedCredentials) > 65536 {
		return errors.New("保存的身份格式无效")
	}
	return nil
}

func identitySecretSettings(i savedIdentity) connectionSettings {
	return connectionSettings{Host: "identity:" + i.ID, Port: "0", User: i.User, KeyPath: i.KeyPath}
}
func identityCredentials(i savedIdentity) (string, string, error) {
	if i.EncryptedCredentials == "" {
		return "", "", nil
	}
	return unprotectProfileCredentials(i.EncryptedCredentials, identitySecretSettings(i))
}
func (d *desktop) identityByID(id string) (savedIdentity, bool) {
	for _, i := range d.identities {
		if i.ID == id {
			return i, true
		}
	}
	return savedIdentity{}, false
}

// Protect the association separately against a modified profile file. An
// identity cannot be silently attached to a different host or user.
func sealIdentityBinding(settings connectionSettings, id string) (string, error) {
	settings.Password, settings.Passphrase = id, ""
	return protectProfileCredentials(settings)
}
func verifyIdentityBinding(p profile) error {
	id, _, err := unprotectProfileCredentials(p.IdentityBinding, p.Settings)
	if err != nil || id != p.IdentityID {
		return errors.New("主机身份关联无法验证，请重新选择身份")
	}
	return nil
}

func (d *desktop) profileCredential(p profile) (connectionSettings, error) {
	s := p.Settings
	if p.IdentityID != "" {
		i, ok := d.identityByID(p.IdentityID)
		if !ok || s.User != i.User || s.KeyPath != i.KeyPath {
			return connectionSettings{}, errors.New("引用的身份不存在或已修改")
		}
		if err := verifyIdentityBinding(p); err != nil {
			return connectionSettings{}, err
		}
		password, passphrase, err := identityCredentials(i)
		if err != nil {
			return connectionSettings{}, errors.New("无法解锁保存的身份")
		}
		s.Password, s.Passphrase = password, passphrase
	} else if p.EncryptedCredentials != "" {
		password, passphrase, err := unprotectProfileCredentials(p.EncryptedCredentials, p.Settings)
		if err != nil {
			return connectionSettings{}, errors.New("无法解锁保存的主机凭据")
		}
		s.Password, s.Passphrase = password, passphrase
	}
	return s, nil
}

func (d *desktop) saveIdentity(id string, draft identityDraft) error {
	if d.profilePath == "" {
		return errors.New("本机连接存储不可用")
	}
	name, user := strings.TrimSpace(draft.Name), strings.TrimSpace(draft.User)
	if id == "" {
		if len(d.identities) >= maxSavedIdentities {
			return errors.New("身份数量已达上限")
		}
		var data [16]byte
		if _, err := rand.Read(data[:]); err != nil {
			return err
		}
		id = hex.EncodeToString(data[:])
		if _, exists := d.identityByID(id); exists {
			return errors.New("无法生成唯一的身份标识，请重试")
		}
	} else if _, ok := d.identityByID(id); !ok {
		return errors.New("身份已不存在，请重新载入")
	}
	for _, other := range d.identities {
		if other.ID != id && strings.EqualFold(other.Name, name) {
			return errors.New("身份名称已存在")
		}
	}
	i := savedIdentity{ID: id, Name: name, User: user, KeyPath: draft.KeyPath}
	if err := validateSavedIdentity(i); err != nil {
		return err
	}
	secretSettings := identitySecretSettings(i)
	secretSettings.Password, secretSettings.Passphrase = draft.Password, draft.Passphrase
	protected, err := protectProfileCredentials(secretSettings)
	if err != nil {
		return err
	}
	i.EncryptedCredentials = protected
	identities := append([]savedIdentity(nil), d.identities...)
	updated := false
	for n := range identities {
		if identities[n].ID == id {
			identities[n], updated = i, true
			break
		}
	}
	if !updated {
		identities = append(identities, i)
	}
	profiles := append([]profile(nil), d.profiles...)
	for n, p := range profiles {
		if p.IdentityID != id {
			continue
		}
		if err := verifyIdentityBinding(p); err != nil {
			return fmt.Errorf("%s：%w", p.Label, err)
		}
		if p.Settings.User != i.User {
			p.Settings.Fingerprint = ""
		}
		p.Settings.User, p.Settings.KeyPath = i.User, i.KeyPath
		p.IdentityBinding, err = sealIdentityBinding(p.Settings, id)
		if err != nil {
			return err
		}
		profiles[n] = p
	}
	originalIdentities, originalProfiles := d.identities, d.profiles
	d.identities, d.profiles = identities, profiles
	if err := d.saveProfiles(); err != nil {
		d.identities, d.profiles = originalIdentities, originalProfiles
		return err
	}
	return nil
}

func (d *desktop) deleteIdentity(id string) error {
	if d.profilePath == "" {
		return errors.New("本机连接存储不可用")
	}
	for _, p := range d.profiles {
		if p.IdentityID == id {
			return errors.New("仍有主机使用此身份，请先解除关联")
		}
	}
	items := make([]savedIdentity, 0, len(d.identities))
	found := false
	for _, i := range d.identities {
		if i.ID == id {
			found = true
		} else {
			items = append(items, i)
		}
	}
	if !found {
		return errors.New("身份已不存在")
	}
	original := d.identities
	d.identities = items
	if err := d.saveProfiles(); err != nil {
		d.identities = original
		return err
	}
	return nil
}

func (d *desktop) chooseProfileIdentity(id string) error {
	if id == "" {
		d.profileIdentityID = ""
		return nil
	}
	i, ok := d.identityByID(id)
	if !ok {
		return errors.New("身份不存在")
	}
	password, passphrase, err := identityCredentials(i)
	if err != nil {
		return errors.New("无法解锁身份")
	}
	if d.settings.User != i.User {
		d.fingerprint, d.expectedFingerprint, d.changedFingerprint = "", "", ""
	}
	d.settings.User, d.settings.KeyPath, d.settings.Password, d.settings.Passphrase = i.User, i.KeyPath, password, passphrase
	d.profileIdentityID, d.profileSecretError = id, false
	return nil
}
