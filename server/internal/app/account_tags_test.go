package app

import (
	"bytes"
	"errors"
	"testing"
)

func TestAccountTagsLegacyWritesConflictsAndBackup(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			save := func(a Account) (Account, error) {
				if scope == "team" {
					return s.store.saveTeamAccount("owner", team.ID, a)
				}
				return s.store.upsertAccount("owner", a)
			}
			list := func() ([]Account, error) {
				if scope == "team" {
					return s.store.listTeamAccounts("owner", team.ID)
				}
				return s.store.listAccounts("owner")
			}
			original, err := save(Account{Label: scope, Host: "example.test", Port: 22, Username: "alice", Password: "test-secret", HostKeyFingerprint: "SHA256:test", Tags: []string{"prod", "database"}})
			if err != nil || s.store.doc.Version != 4 {
				t.Fatal("tagged account save failed", err)
			}
			legacy := original
			legacy.Tags = nil
			legacy.Label = "legacy update"
			updated, err := save(legacy)
			if err != nil || len(updated.Tags) != 2 {
				t.Fatal("legacy client lost tags", err)
			}
			stale := original
			stale.Tags = []string{"stale"}
			stale.ExpectedUpdatedAt = original.UpdatedAt
			if _, err = save(stale); !errors.Is(err, errAccountConflict) {
				t.Fatal("stale tag update accepted", err)
			}
			clear := updated
			clear.Tags = []string{}
			clear.ExpectedUpdatedAt = updated.UpdatedAt
			cleared, err := save(clear)
			if err != nil || len(cleared.Tags) != 0 {
				t.Fatal("explicit clear failed", err)
			}
			cleared.Tags = []string{"protected-tag"}
			cleared.ExpectedUpdatedAt = cleared.UpdatedAt
			if _, err = save(cleared); err != nil {
				t.Fatal(err)
			}
			accounts, err := list()
			if err != nil || len(accounts) != 1 || len(accounts[0].Tags) != 1 || accounts[0].Tags[0] != "protected-tag" {
				t.Fatal("tags not stored", err)
			}
		})
	}
	backup, err := s.store.exportBackup()
	if err != nil || bytes.Contains(backup, []byte("protected-tag")) {
		t.Fatal("backup failed or leaked tag", err)
	}
	if err = s.store.importBackup(backup, "owner"); err != nil {
		t.Fatal(err)
	}
	owner, err := s.store.listAccounts("owner")
	if err != nil || len(owner) != 1 || owner[0].Tags[0] != "protected-tag" {
		t.Fatal("backup lost tags", err)
	}
	invalid := s.store.doc
	invalid.Version = 3
	if validateAccountTagsDocument(invalid) == nil {
		t.Fatal("old storage version accepted tags")
	}
	path := s.store.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(path, "test-master-key-which-is-long")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	owner, err = reopened.listAccounts("owner")
	if err != nil || len(owner) != 1 || owner[0].Tags[0] != "protected-tag" {
		t.Fatal("tags did not survive restart", err)
	}
}

func TestAccountTagsValidation(t *testing.T) {
	for _, tags := range [][]string{{"duplicate", "DUPLICATE"}, {"bad,tag"}, {"bad\x00tag"}, {" trailing "}} {
		if validAccountTags(tags) {
			t.Fatal("invalid tags accepted", tags)
		}
	}
}
