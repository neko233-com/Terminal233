//go:build windows

package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func backupFixture(t *testing.T) (*desktop, localArchiveDocument) {
	t.Helper()
	dir := t.TempDir()
	d := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := d.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := d.saveIdentity("", identityDraft{Name: "工作身份", User: "alice", Password: "identity-secret", Passphrase: "key-phrase", KeyPath: "C:/keys/work"}); err != nil {
		t.Fatal(err)
	}
	d.profileGroups = []string{"生产", "生产/数据库"}
	d.settings = connectionSettings{Host: "db.example", Port: "22", User: "root", Password: "host-secret", Fingerprint: "SHA256:trusted"}
	d.profileGroup = "生产/数据库"
	d.profileLabel = "数据库"
	d.profileTags = "数据库, 生产"
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	d.profiles[0].Settings.Fingerprint = "SHA256:trusted"
	d.profiles[0].Favorite = true
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	d.selectedProfile = ""
	d.settings = connectionSettings{Host: "web.example", Port: "22"}
	if err := d.chooseProfileIdentity(d.identities[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	hostID, err := d.ensureForwardHostID(d.profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := newRandomID()
	if err != nil {
		t.Fatal(err)
	}
	rule := savedForwardRule{ID: id, Name: "数据库", HostID: hostID, Mode: "local", ListenPort: "0", TargetHost: "127.0.0.1", TargetPort: "5432"}
	if err := d.saveForwardRule(rule, nil); err != nil {
		t.Fatal(err)
	}
	doc, err := d.snapshotLocalArchive()
	if err != nil {
		t.Fatal(err)
	}
	return d, doc
}

func TestLocalArchiveEncryptedRoundTripAndImport(t *testing.T) {
	_, doc := backupFixture(t)
	data, err := encodeLocalArchive(doc, "long-backup-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"identity-secret", "host-secret", "key-phrase", "db.example"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("plaintext leaked into encrypted backup")
		}
	}
	if _, err := decodeLocalArchive(data, "incorrect-password"); err == nil {
		t.Fatal("accepted incorrect password")
	}
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)/2] ^= 1
	if _, err := decodeLocalArchive(tampered, "long-backup-password"); err == nil {
		t.Fatal("accepted modified backup")
	}
	restored, err := decodeLocalArchive(data, "long-backup-password")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := target.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := target.saveIdentity("", identityDraft{Name: "工作身份", User: "existing", Password: "existing-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := target.importLocalArchive(restored); err != nil {
		t.Fatal(err)
	}
	if len(target.profiles) != 2 || len(target.identities) != 2 || target.identities[1].Name == "工作身份" || len(target.forwardRules) != 1 || len(target.profileGroups) != 2 {
		t.Fatal("backup did not restore all object types")
	}
	if target.profiles[0].Settings.Fingerprint != "" || target.forwardRules[0].HostID != target.profiles[0].StableID || target.profiles[0].StableID == doc.Profiles[0].StableID {
		t.Fatal("import trusted a foreign key or failed to remap the rule")
	}
	first, err := target.profileCredential(target.profiles[0])
	if err != nil || first.Password != "host-secret" {
		t.Fatalf("host credential not restored: %v", err)
	}
	second, err := target.profileCredential(target.profiles[1])
	if err != nil || second.Password != "identity-secret" || second.Passphrase != "key-phrase" {
		t.Fatalf("linked identity not restored: %v", err)
	}
	stored, err := os.ReadFile(target.profilePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"host-secret", "identity-secret", "key-phrase", "existing-secret"} {
		if strings.Contains(string(stored), secret) {
			t.Fatal("plaintext secret stored in local profiles")
		}
	}
	var reopened desktop
	reopened.forwardRulesPath = target.forwardRulesPath
	if err := reopened.loadProfiles(target.profilePath); err != nil {
		t.Fatal(err)
	}
	rules, err := readForwardRules(target.forwardRulesPath)
	if err != nil || len(rules.Items) != 1 || rules.Items[0].HostID != reopened.profiles[0].StableID {
		t.Fatal("restored links did not survive reload")
	}
}

func TestLocalArchiveImportRejectsStaleProfilesWithoutWritingRules(t *testing.T) {
	_, doc := backupFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	rules := filepath.Join(dir, "forward-rules.json")
	a, b := &desktop{forwardRulesPath: rules}, &desktop{forwardRulesPath: rules}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	a.settings = connectionSettings{Host: "existing.example", Port: "22", User: "alice"}
	if err := a.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	if err := b.importLocalArchive(doc); err == nil {
		t.Fatal("stale import overwrote newer data")
	}
	if len(b.profiles) != 0 {
		t.Fatal("failed import left staged profiles")
	}
	file, err := readForwardRules(rules)
	if err != nil || len(file.Items) != 0 {
		t.Fatal("failed import wrote forwarding rules")
	}
	var verify desktop
	if err := verify.loadProfiles(path); err != nil || len(verify.profiles) != 1 || verify.profiles[0].Settings.Host != "existing.example" {
		t.Fatal("concurrent profiles were lost")
	}
}

func TestLocalArchiveRollsBackWhenRuleCommitFails(t *testing.T) {
	_, doc := backupFixture(t)
	dir := t.TempDir()
	target := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := target.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	target.settings = connectionSettings{Host: "existing.example", Port: "22", User: "alice"}
	if err := target.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	failAfterProfiles := func(path string, change func(*forwardRulesFile) error) (forwardRulesFile, error) {
		file, err := readForwardRules(path)
		if err != nil {
			return file, err
		}
		if err = change(&file); err != nil {
			return file, err
		}
		return forwardRulesFile{}, errors.New("simulated rule write failure")
	}
	if err := target.importLocalArchiveWith(doc, failAfterProfiles); err == nil || !strings.Contains(err.Error(), "已回滚") {
		t.Fatal("failed rule write was not rolled back")
	}
	var restored desktop
	if err := restored.loadProfiles(target.profilePath); err != nil || len(restored.profiles) != 1 || restored.profiles[0].Settings.Host != "existing.example" {
		t.Fatal("rollback did not restore original profiles")
	}
	if len(target.profiles) != 1 || len(target.identities) != 0 {
		t.Fatal("rollback left staged items in memory")
	}
}

func TestLocalArchiveRollbackNeverOverwritesConcurrentEdit(t *testing.T) {
	_, doc := backupFixture(t)
	dir := t.TempDir()
	target := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := target.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	target.settings = connectionSettings{Host: "existing.example", Port: "22", User: "alice"}
	if err := target.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	concurrent := func(path string, change func(*forwardRulesFile) error) (forwardRulesFile, error) {
		file, err := readForwardRules(path)
		if err != nil {
			return file, err
		}
		if err = change(&file); err != nil {
			return file, err
		}
		var other desktop
		if err = other.loadProfiles(target.profilePath); err != nil {
			return file, err
		}
		other.profiles[0].Label = "other-window"
		if err = other.saveProfiles(); err != nil {
			return file, err
		}
		return forwardRulesFile{}, errors.New("simulated rule write failure")
	}
	if err := target.importLocalArchiveWith(doc, concurrent); err == nil || !target.profileStoreConflict || !strings.Contains(err.Error(), "自动回滚失败") {
		t.Fatal("concurrent edit did not block rollback")
	}
	var restored desktop
	if err := restored.loadProfiles(target.profilePath); err != nil || restored.profiles[0].Label != "other-window" || len(restored.profiles) != 1+len(doc.Profiles) {
		t.Fatal("rollback overwrote concurrent edit")
	}
}

func TestLocalArchiveRejectsDanglingIdentity(t *testing.T) {
	_, doc := backupFixture(t)
	doc.Profiles[0].IdentityID = "00000000000000000000000000000000"
	if _, err := encodeLocalArchive(doc, "long-backup-password"); err == nil {
		t.Fatal("accepted dangling identity reference")
	}
}

func TestLocalArchiveRejectsInjectedHostTrust(t *testing.T) {
	plain := []byte(`{"Version":1,"Profiles":[{"Host":"host.example","Port":"22","User":"alice","Fingerprint":"SHA256:untrusted"}]}`)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	aead, err := archiveCipher("long-backup-password", salt)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cloudArchiveEnvelope{Format: localArchiveFormat, Salt: salt, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plain, []byte(localArchiveFormat))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeLocalArchive(data, "long-backup-password"); err == nil {
		t.Fatal("untrusted host fingerprint field was accepted")
	}
}
