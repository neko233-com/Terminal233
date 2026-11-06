package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSixteenTerminalGridAndLegacyTemplateCompatibility(t *testing.T) {
	terms := groupTestTerminals(t, 16)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0]}
	ids := make([]int, 16)
	for i := range ids {
		ids[i] = i + 1
		d.sessions = append(d.sessions, &workspaceSession{id: ids[i], label: fmt.Sprintf("session-%02d", ids[i]), term: terms[i]})
	}
	// An independent SFTP session does not reduce the terminal capacity.
	d.sessions = append(d.sessions, &workspaceSession{id: 100, fileOnly: true})
	if !d.terminalLimitReached() || d.fileLimitReached() {
		t.Fatal("terminal and SFTP capacities were mixed")
	}
	if err := d.createTerminalGroup("sixteen", ids, 1, true); err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1600, 1200)
	for _, id := range ids {
		if _, ok := view.Find(fmt.Sprintf("终端：session-%02d", id)); !ok {
			t.Fatalf("grid omitted terminal %d", id)
		}
	}
	g := d.terminalGroupFor(1)
	g.columnPerc[14] = 42
	w, err := d.workspaceSnapshot("sixteen")
	if err != nil || len(w.Entries) != 16 || len(w.ColumnPerc) != 16 || w.ColumnPerc[14] != 42 {
		t.Fatal("16-terminal snapshot invalid", err)
	}
	path := filepath.Join(t.TempDir(), "workspaces.json")
	saved, err := updateWorkspaceTemplates(path, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil })
	if err != nil || saved.Version != 4 {
		t.Fatal("new format not selected", err)
	}
	saved, err = readWorkspaceTemplates(path)
	if err != nil || saved.Items[0].ColumnPerc[14] != 42 {
		t.Fatal("new format did not round trip", err)
	}
	// A version-3 file cannot claim new-format content, so an old client never overwrites it.
	saved.Version = 3
	data, _ := json.Marshal(saved)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readWorkspaceTemplates(path); err == nil {
		t.Fatal("extended workspace accepted under legacy format")
	}
	// A new client still writes the old layout format for up to 12 sessions.
	g.members = g.members[:12]
	w, err = d.workspaceSnapshot("legacy")
	if err != nil || len(w.ColumnPerc) != 12 {
		t.Fatal("legacy layout was changed", err)
	}
}

func TestWorkspaceLimitsAndOldCloudService(t *testing.T) {
	d := &desktop{}
	for i := 0; i < maxFileSessions; i++ {
		d.sessions = append(d.sessions, &workspaceSession{fileOnly: true})
	}
	if !d.fileLimitReached() || d.terminalLimitReached() {
		t.Fatal("file capacity blocked terminals")
	}
	w := sampleWorkspace("0123456789abcdef0123456789abcdef", "workspace")
	w.Entries = make([]workspaceTemplateEntry, 16)
	for i := range w.Entries {
		w.Entries[i] = workspaceTemplateEntry{Kind: "local"}
	}
	w.ViewMode = "grid"
	w.RowPerc, w.ColumnPerc = make([]float32, 16), make([]float32, 16)
	if _, err := d.resolveWorkspace(w); err != nil {
		t.Fatal("file sessions blocked workspace restore", err)
	}
	c, err := newCloudClient("https://sync.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = portableWorkspace(w, c, "user"); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatal("old server accepted extended workspace", err)
	}
	c.cloudWorkspaces16 = true
	if _, err = portableWorkspace(w, c, "user"); err != nil {
		t.Fatal("capable server rejected workspace", err)
	}
	w.Entries = append(w.Entries, workspaceTemplateEntry{Kind: "local"})
	if validateSavedWorkspace(w) == nil {
		t.Fatal("accepted more than 16 terminals")
	}
}
