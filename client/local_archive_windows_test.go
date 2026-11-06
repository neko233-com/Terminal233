//go:build windows

package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestLocalArchiveInterruptedRecovery(t *testing.T) {
	_, doc := backupFixture(t)
	for _, stage := range []string{"journal", "profiles", "rules"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			target := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
			if err := target.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
				t.Fatal(err)
			}
			target.settings = connectionSettings{Host: "existing.example", Port: "22", User: "alice"}
			if err := target.saveCurrentProfile(); err != nil {
				t.Fatal(err)
			}
			err := target.importLocalArchiveWith(doc, func(step string) error {
				if step == stage {
					return errors.New("interrupted")
				}
				return nil
			})
			if err == nil || !target.profileStoreBlocked {
				t.Fatal("interruption not blocked", err)
			}
			data, err := os.ReadFile(localImportJournalPath(target.profilePath))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"identity-secret", "key-phrase", "host-secret"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("journal contains plaintext secret")
				}
			}
			var other desktop
			if other.loadProfiles(target.profilePath) == nil {
				t.Fatal("incomplete profiles exposed")
			}
			if _, err := readForwardRules(target.forwardRulesPath); err == nil {
				t.Fatal("incomplete rules exposed")
			}
			if _, err := updateForwardRules(target.forwardRulesPath, func(*forwardRulesFile) error { return nil }); err == nil {
				t.Fatal("pending rules modified")
			}
			recovered, err := recoverLocalImport(target.profilePath, target.forwardRulesPath)
			if err != nil || !recovered {
				t.Fatal("recovery failed", err)
			}
			if recovered, err = recoverLocalImport(target.profilePath, target.forwardRulesPath); err != nil || recovered {
				t.Fatal("recovery not idempotent", err)
			}
			if err = other.loadProfiles(target.profilePath); err != nil || len(other.profiles) != 1+len(doc.Profiles) || other.profiles[0].Settings.Host != "existing.example" {
				t.Fatal("profiles not recovered", err)
			}
			rules, err := readForwardRules(target.forwardRulesPath)
			if err != nil || len(rules.Items) != len(doc.Rules) || rules.Items[0].HostID != other.profiles[1].StableID {
				t.Fatal("rules not recovered", err)
			}
		})
	}
}

func TestLocalArchiveRecoveryPreservesExternalChanges(t *testing.T) {
	_, doc := backupFixture(t)
	dir := t.TempDir()
	target := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := target.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := target.importLocalArchiveWith(doc, func(step string) error {
		if step == "profiles" {
			return errors.New("interrupted")
		}
		return nil
	}); err == nil {
		t.Fatal("expected interruption")
	}
	external := []byte(`{"Version":1,"Items":[]}`)
	if err := os.WriteFile(target.profilePath, external, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverLocalImport(target.profilePath, target.forwardRulesPath); err == nil {
		t.Fatal("external modification was overwritten")
	}
	actual, _ := os.ReadFile(target.profilePath)
	if string(actual) != string(external) {
		t.Fatal("external data changed")
	}
	if _, err := os.Stat(localImportJournalPath(target.profilePath)); err != nil {
		t.Fatal("recovery data lost", err)
	}
	if _, err := os.Stat(target.forwardRulesPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rule file unexpectedly changed", err)
	}
}

func TestLocalArchiveRecoveryAfterProcessExit(t *testing.T) {
	if stage := os.Getenv("NEKOSSH_RECOVERY_CHILD"); stage != "" {
		dir := os.Getenv("NEKOSSH_RECOVERY_DIR")
		target := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
		if err := target.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
			t.Fatal(err)
		}
		const hostID = "11111111111111111111111111111111"
		doc := localArchiveDocument{Version: 1, Profiles: []localArchiveProfile{{StableID: hostID, Host: "host.example", Port: "22", User: "alice", Password: "synthetic-secret"}}, Rules: []savedForwardRule{{ID: "22222222222222222222222222222222", HostID: hostID, Name: "test", Mode: "local", ListenPort: "0", TargetHost: "localhost", TargetPort: "5432"}}}
		if err := target.importLocalArchiveWith(doc, func(step string) error {
			if step == stage {
				os.Exit(73)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("interrupt checkpoint was not reached")
	}
	for _, stage := range []string{"journal", "profiles", "rules"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestLocalArchiveRecoveryAfterProcessExit$")
			cmd.Env = append(os.Environ(), "NEKOSSH_RECOVERY_CHILD="+stage, "NEKOSSH_RECOVERY_DIR="+dir)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			err := cmd.Run()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 73 {
				t.Fatalf("child did not exit at checkpoint: %v", err)
			}
			profilePath, rulesPath := filepath.Join(dir, "profiles.json"), filepath.Join(dir, "forward-rules.json")
			if recovered, err := recoverLocalImport(profilePath, rulesPath); err != nil || !recovered {
				t.Fatal("failed to recover exited process", err)
			}
			var d desktop
			if err := d.loadProfiles(profilePath); err != nil || len(d.profiles) != 1 {
				t.Fatal("profile not recovered", err)
			}
			rules, err := readForwardRules(rulesPath)
			if err != nil || len(rules.Items) != 1 || rules.Items[0].HostID != d.profiles[0].StableID {
				t.Fatal("rule not recovered", err)
			}
		})
	}
}

func TestLocalArchiveRecoveryRejectsCorruptJournal(t *testing.T) {
	_, doc := backupFixture(t)
	dir := t.TempDir()
	d := &desktop{forwardRulesPath: filepath.Join(dir, "forward-rules.json")}
	if err := d.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := d.importLocalArchiveWith(doc, func(step string) error {
		if step == "journal" {
			return errors.New("interrupted")
		}
		return nil
	}); err == nil {
		t.Fatal("expected interruption")
	}
	path := localImportJournalPath(d.profilePath)
	if err := os.WriteFile(path, []byte(`{"Version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverLocalImport(d.profilePath, d.forwardRulesPath); err == nil {
		t.Fatal("corrupt recovery data accepted")
	}
	if _, err := os.Stat(d.profilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("profile changed", err)
	}
	if _, err := os.Stat(d.forwardRulesPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rules changed", err)
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
