package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const localArchiveFormat = "nekossh-local-v1"
const maxLocalArchivePlain = 4 << 20
const maxLocalArchiveFile = 6 << 20

type localArchiveIdentity struct{ ID, Name, User, KeyPath, Password, Passphrase string }
type localArchiveProfile struct {
	StableID, Group, Label, Host, Port, User, KeyPath, Password, Passphrase, IdentityID string
	Tags                                                                                []string
	Favorite                                                                            bool
}
type localArchiveDocument struct {
	Version    int
	Groups     []string
	Identities []localArchiveIdentity
	Profiles   []localArchiveProfile
	Rules      []savedForwardRule
}

func validLocalArchiveGroup(group string) bool {
	if group == "" {
		return true
	}
	if len(group) > 512 || strings.Count(group, "/") >= 8 {
		return false
	}
	for _, part := range strings.Split(group, "/") {
		if !validGroupName(part) {
			return false
		}
	}
	return true
}

func validateLocalArchive(doc localArchiveDocument) error {
	if doc.Version != 1 || len(doc.Groups) > 10000 || len(doc.Identities) > maxSavedIdentities || len(doc.Profiles) > 10000 || len(doc.Rules) > maxSavedForwardRules {
		return errors.New("本机备份版本或数量无效")
	}
	groups := map[string]bool{}
	for _, g := range doc.Groups {
		if g == "" || !validLocalArchiveGroup(g) || groups[g] {
			return errors.New("本机备份分组无效")
		}
		groups[g] = true
	}
	identities := map[string]localArchiveIdentity{}
	for _, i := range doc.Identities {
		if validateSavedIdentity(savedIdentity{ID: i.ID, Name: i.Name, User: i.User, KeyPath: i.KeyPath}) != nil || len(i.Password)+len(i.Passphrase) > 16384 {
			return errors.New("本机备份身份无效")
		}
		if _, exists := identities[i.ID]; exists {
			return errors.New("本机备份身份重复")
		}
		identities[i.ID] = i
	}
	hostIDs := map[string]bool{}
	for _, p := range doc.Profiles {
		s := connectionSettings{Host: p.Host, Port: p.Port, User: p.User, KeyPath: p.KeyPath}
		if _, err := s.address(); err != nil || !validWorkspaceText(p.Label, 1024) || !validWorkspaceText(p.Host, 1024) || !validWorkspaceText(p.User, 256) || !validWorkspaceText(p.KeyPath, 4096) || !validLocalArchiveGroup(p.Group) || !validStoredProfileTags(p.Tags) || len(p.Password)+len(p.Passphrase) > 16384 {
			return errors.New("本机备份主机无效")
		}
		if p.StableID != "" {
			if !validRandomID(p.StableID) || hostIDs[p.StableID] {
				return errors.New("本机备份主机标识无效")
			}
			hostIDs[p.StableID] = true
		}
		if p.IdentityID != "" {
			identity, ok := identities[p.IdentityID]
			if !ok || identity.User != p.User || identity.KeyPath != p.KeyPath || p.Password != "" || p.Passphrase != "" {
				return errors.New("本机备份身份关联无效")
			}
		}
	}
	ruleIDs := map[string]bool{}
	for _, r := range doc.Rules {
		if validateForwardRule(r) != nil || ruleIDs[r.ID] {
			return errors.New("本机备份转发规则无效")
		}
		ruleIDs[r.ID] = true
	}
	return nil
}

// Snapshot only a current profile revision and a locked rule file. All secret
// values are unprotected in memory solely for the encrypted portable backup.
func (d *desktop) snapshotLocalArchive() (localArchiveDocument, error) {
	var doc localArchiveDocument
	if d.profilePath == "" || d.forwardRulesPath == "" || d.profileStoreBlocked {
		return doc, errors.New("本机存储不可用")
	}
	if err := os.MkdirAll(filepath.Dir(d.profilePath), 0700); err != nil {
		return doc, err
	}
	if err := os.MkdirAll(filepath.Dir(d.forwardRulesPath), 0700); err != nil {
		return doc, err
	}
	lock := flock.New(d.profilePath + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return doc, err
	}
	if !locked {
		return doc, errors.New("另一个窗口正在保存主机")
	}
	defer lock.Unlock()
	data, err := os.ReadFile(d.profilePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return doc, err
	}
	exists := err == nil
	if exists != d.profileExists || (exists && sha256.Sum256(data) != d.profileRevision) {
		return doc, errors.New("主机已被其他窗口修改，请重新载入")
	}
	ruleLock := flock.New(d.forwardRulesPath + ".lock")
	locked, err = ruleLock.TryLock()
	if err != nil {
		return doc, err
	}
	if !locked {
		return doc, errors.New("另一个窗口正在保存转发规则")
	}
	defer ruleLock.Unlock()
	rules, err := readForwardRules(d.forwardRulesPath)
	if err != nil {
		return doc, err
	}
	doc = localArchiveDocument{Version: 1, Groups: append([]string(nil), d.profileGroups...), Rules: append([]savedForwardRule(nil), rules.Items...)}
	for _, i := range d.identities {
		password, phrase, err := identityCredentials(i)
		if err != nil {
			return localArchiveDocument{}, errors.New("无法解锁身份，备份未生成")
		}
		doc.Identities = append(doc.Identities, localArchiveIdentity{ID: i.ID, Name: i.Name, User: i.User, KeyPath: i.KeyPath, Password: password, Passphrase: phrase})
	}
	for _, p := range d.profiles {
		s, err := d.profileCredential(p)
		if err != nil {
			return localArchiveDocument{}, errors.New("无法解锁主机凭据，备份未生成")
		}
		item := localArchiveProfile{StableID: p.StableID, Group: p.Group, Label: p.Label, Host: s.Host, Port: s.Port, User: s.User, KeyPath: s.KeyPath, IdentityID: p.IdentityID, Tags: append([]string(nil), p.Tags...), Favorite: p.Favorite}
		if p.IdentityID == "" {
			item.Password, item.Passphrase = s.Password, s.Passphrase
		}
		doc.Profiles = append(doc.Profiles, item)
	}
	if err := validateLocalArchive(doc); err != nil {
		return localArchiveDocument{}, err
	}
	return doc, nil
}

func encodeLocalArchive(doc localArchiveDocument, password string) ([]byte, error) {
	if err := validateLocalArchive(doc); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(doc)
	if err != nil || len(plain) > maxLocalArchivePlain {
		return nil, errors.New("本机备份内容过大或无法编码")
	}
	defer clear(plain)
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := archiveCipher(password, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return json.Marshal(cloudArchiveEnvelope{Format: localArchiveFormat, Salt: salt, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plain, []byte(localArchiveFormat))})
}
func decodeLocalArchive(data []byte, password string) (localArchiveDocument, error) {
	var empty localArchiveDocument
	if len(data) > maxLocalArchiveFile {
		return empty, errors.New("本机备份文件过大")
	}
	var envelope cloudArchiveEnvelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&envelope) != nil || dec.Decode(&struct{}{}) != io.EOF || envelope.Format != localArchiveFormat || len(envelope.Salt) != 16 || len(envelope.Nonce) != 12 || len(envelope.Ciphertext) < 16 || len(envelope.Ciphertext) > maxLocalArchivePlain+16 {
		return empty, errors.New("本机备份格式无效")
	}
	aead, err := archiveCipher(password, envelope.Salt)
	if err != nil {
		return empty, err
	}
	plain, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, []byte(localArchiveFormat))
	if err != nil {
		return empty, errors.New("备份密码错误或文件已损坏")
	}
	defer clear(plain)
	var doc localArchiveDocument
	dec = json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return empty, errors.New("本机备份内容无效")
	}
	if err := validateLocalArchive(doc); err != nil {
		return empty, err
	}
	return doc, nil
}
func readLocalArchive(path, password string) (localArchiveDocument, error) {
	f, err := os.Open(path)
	if err != nil {
		return localArchiveDocument{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLocalArchiveFile+1))
	if err != nil {
		return localArchiveDocument{}, err
	}
	return decodeLocalArchive(data, password)
}

func importedIdentityName(name string, existing []savedIdentity) (string, error) {
	used := func(candidate string) bool {
		for _, i := range existing {
			if strings.EqualFold(i.Name, candidate) {
				return true
			}
		}
		return false
	}
	if !used(name) {
		return name, nil
	}
	for n := 2; n <= maxSavedIdentities+1; n++ {
		suffix := fmt.Sprintf("（导入 %d）", n)
		runes := []rune(name)
		for len(runes) > 0 && len(string(runes))+len(suffix) > 120 {
			runes = runes[:len(runes)-1]
		}
		candidate := string(runes) + suffix
		if !used(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("无法为导入身份生成名称")
}
