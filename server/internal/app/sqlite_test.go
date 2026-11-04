package app

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteEncryptedPersistenceAndLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "legacy.db")
	key := "a long test master key with enough chars"
	doc := storeDocument{Version: 1, Users: map[string]userRecord{"alice": {Username: "alice"}}, Accounts: map[string][]storedAccount{"alice": {}}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	original, err := encryptKeyForTest(key, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, original, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(p, key)
	if err != nil {
		t.Fatal(err)
	}
	if store.doc.Users["alice"].Username != "alice" {
		t.Fatal("migration lost user")
	}
	backup, err := os.ReadFile(p + ".legacy-backup")
	if err != nil || string(backup) != string(original) {
		t.Fatal("legacy backup missing or altered")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "SQLite format 3\x00") || strings.Contains(string(data), "alice") {
		t.Fatal("database is not SQLite or exposes plaintext")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(p, key)
	if err != nil || reopened.doc.Users["alice"].Username != "alice" {
		t.Fatalf("reload failed: %v", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = openStore(p, "incorrect master key"); err == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestLegacyMigrationDoesNotOverwriteExistingBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "legacy.db")
	key := "a long test master key with enough chars"
	raw, _ := json.Marshal(storeDocument{Version: 1, Users: map[string]userRecord{}, Accounts: map[string][]storedAccount{}})
	old, _ := encryptKeyForTest(key, raw)
	if err := os.WriteFile(p, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".legacy-backup", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(p, key); err == nil {
		t.Fatal("overwrote existing backup")
	}
	data, _ := os.ReadFile(p)
	if string(data) != string(old) {
		t.Fatal("legacy data modified on failed migration")
	}
}

func encryptKeyForTest(master string, data []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(master))
	return encrypt(key[:], data)
}
func TestConcurrentStoreInstancesAreRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.sqlite")
	key := "a long test master key with enough chars"
	first, err := openStore(p, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	if second, err := openStore(p, key); err == nil {
		_ = second.Close()
		t.Fatal("second server acquired active database")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(p, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.userEnabled("alice") {
		t.Fatal("reopen lost user")
	}
}
func TestExternalDatabaseChangeInvalidatesSession(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.sqlite")
	key := "a long test master key with enough chars"
	first, err := openStore(p, key)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = first.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE encrypted_state SET revision=revision+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if first.userEnabled("alice") {
		t.Fatal("external modification did not invalidate sessions")
	}
}
func TestMissingDatabaseWithLegacyBackupFailsClosed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.sqlite")
	if err := os.WriteFile(p+".legacy-backup", []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(p, "a long test master key with enough chars"); err == nil {
		t.Fatal("started empty despite existing migration backup")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("created replacement database")
	}
}
