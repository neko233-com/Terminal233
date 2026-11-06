package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalEditRoundTripAndConflict(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err = os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.ToSlash(dir)
	contents, initial, err := loadExternalEdit(shell, remoteDir, "note.txt")
	if err != nil || string(contents) != "original" {
		t.Fatalf("load: %q %v", contents, err)
	}
	saved, err := saveExternalEdit(shell, remoteDir, "note.txt", initial, []byte("edited"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "edited" {
		t.Fatal("edited contents not saved")
	}
	if saved.digest == initial.digest {
		t.Fatal("baseline was not updated")
	}
	if err = os.WriteFile(target, []byte("other writer"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = saveExternalEdit(shell, remoteDir, "note.txt", saved, []byte("my change")); !errors.Is(err, errEditConflict) {
		t.Fatalf("conflict not detected: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "other writer" {
		t.Fatal("overwrote conflicting content")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".nekossh-edit-") {
			t.Fatal("staging file leaked")
		}
	}
}

func TestExternalEditRejectsUnsafeNamesAndLargeFiles(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	dir := t.TempDir()
	large, err := os.Create(filepath.Join(dir, "large"))
	if err != nil {
		t.Fatal(err)
	}
	if err = large.Truncate(maxExternalEditSize + 1); err != nil {
		t.Fatal(err)
	}
	_ = large.Close()
	if _, _, err = loadExternalEdit(shell, filepath.ToSlash(dir), "large"); err == nil {
		t.Fatal("large file accepted")
	}
	if _, _, err = loadExternalEdit(shell, filepath.ToSlash(dir), "../outside"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestExternalEditRefreshesOnlyMatchingPaneMetadata(t *testing.T) {
	shell, other := &remoteShell{}, &remoteShell{}
	d := &desktop{shell: shell, fileDir: "/one", files: []remoteFile{{Name: "a", Size: 1}, {Name: "b", Size: 2}}, secondaryPane: secondaryRemotePane{shell: shell, directory: "/one", files: []remoteFile{{Name: "a", Size: 1}}}, sessions: []*workspaceSession{{shell: other, fileDir: "/one", files: []remoteFile{{Name: "a", Size: 1}}}}}
	d.applyExternalEditInfo(shell, "/one", "a", editSnapshot{size: 9, modified: 123})
	if d.files[0].Size != 9 || d.secondaryPane.files[0].Modified != 123 || d.files[1].Size != 2 || d.sessions[0].files[0].Size != 1 {
		t.Fatal("editor refreshed the wrong file list")
	}
}
