package main

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestWorkspaceGroupSaveUpdateAndConflict(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], workspaceTemplatesPath: filename, sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0]}, {id: 2, label: "two", term: terms[1]}}}
	if err := d.createTerminalGroup("work", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	g := d.terminalGroupFor(1)
	v := ui.NewTester(d.view, 1400, 900)
	if !v.HasText("保存工作区：work") {
		t.Fatal("new workspace has no save indicator")
	}
	if err := v.Click("保存工作区：work"); err != nil {
		t.Fatal(err)
	}
	file, err := readWorkspaceTemplates(filename)
	if err != nil || len(file.Items) != 1 || g.savedLocal == nil || d.workspaceGroupDirty(g) {
		t.Fatalf("initial save failed: %v %s", err, d.status)
	}
	originalID := file.Items[0].ID
	v.Key(ui.Cmd|ui.Alt, ui.KeyM)
	if !g.grid || !d.workspaceGroupDirty(g) {
		t.Fatal("workspace layout shortcut did not mark the group dirty")
	}
	if supportsTerminalBroadcast(terms[0]) {
		v.Key(ui.Cmd|ui.Alt, ui.KeyB)
		if !g.broadcast {
			t.Fatal("broadcast shortcut did not enable broadcast")
		}
		v.Key(ui.Cmd|ui.Alt, ui.KeyB)
		if g.broadcast {
			t.Fatal("broadcast shortcut did not disable broadcast")
		}
	}
	v.Key(ui.Cmd, ui.KeyS)
	file, err = readWorkspaceTemplates(filename)
	if err != nil || len(file.Items) != 1 || file.Items[0].ID != originalID || file.Items[0].ViewMode != "grid" || d.workspaceGroupDirty(g) {
		t.Fatalf("update failed: %v %s", err, d.status)
	}
	before := cloneSavedWorkspace(file.Items[0])
	external := cloneSavedWorkspace(before)
	external.Name = "changed elsewhere"
	if _, err = replaceLocalGroupTemplate(filename, &before, external); err != nil {
		t.Fatal(err)
	}
	g.grid = false
	v.Key(ui.Cmd, ui.KeyS)
	after, err := readWorkspaceTemplates(filename)
	if err != nil || !reflect.DeepEqual(after.Items[0], external) || !strings.Contains(d.status, "其他窗口修改") || !d.workspaceGroupDirty(g) {
		t.Fatalf("stale update overwrote another window: %v %s", err, d.status)
	}
}

func TestWorkspaceSaveShortcutDoesNotReachTerminal(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	term, err := terminal.New(terminal.Options{Conn: conn, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	other := groupTestTerminals(t, 1)[0]
	d := &desktop{workspace: "terminal", currentSession: 1, term: term, pendingTerminalFocus: 1, workspaceTemplatesPath: filepath.Join(t.TempDir(), "workspaces.json"), sessions: []*workspaceSession{{id: 1, label: "one", term: term}, {id: 2, label: "two", term: other}}}
	if err := d.createTerminalGroup("work", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	v := ui.NewTester(d.view, 1400, 900)
	if !v.Focused("终端：one") {
		t.Fatal("terminal was not focused")
	}
	v.Key(ui.Cmd, ui.KeyS)
	if d.terminalGroupFor(1).savedLocal == nil {
		t.Fatal("save shortcut was not handled")
	}
	if err := peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := peer.Read(buf); n != 0 || err == nil {
		t.Fatalf("save shortcut was also sent to the terminal: %x", buf[:n])
	}
	d.busy = true
	v.Key(ui.Cmd, ui.KeyS)
	if !strings.Contains(d.status, "等待") {
		t.Fatal("busy save did not report the running operation")
	}
	if err := peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := peer.Read(buf); n != 0 || err == nil {
		t.Fatalf("busy save shortcut reached the terminal: %x", buf[:n])
	}
}

func TestWorkspaceGroupDirtyNormalizesDefaultLayout(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	d := &desktop{workspace: "terminal", currentSession: 1, term: terms[0], sessions: []*workspaceSession{{id: 1, label: "one", term: terms[0]}, {id: 2, label: "two", term: terms[1]}}}
	if err := d.createTerminalGroup("work", []int{1, 2}, 1, true); err != nil {
		t.Fatal(err)
	}
	g := d.terminalGroupFor(1)
	g.savedLocal = &savedWorkspace{}
	g.savedSignature = d.workspaceGroupSignature(g)
	_ = ui.NewTester(d.view, 1400, 900)
	if d.workspaceGroupDirty(g) {
		t.Fatal("rendering default split marked a saved workspace dirty")
	}
	g.columnPerc[0] = 63
	if !d.workspaceGroupDirty(g) {
		t.Fatal("resizing did not mark workspace dirty")
	}
}

func TestWorkspaceGroupReplacementRejectsConflicts(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	first := sampleWorkspace("00000000000000000000000000000001", "one")
	second := sampleWorkspace("00000000000000000000000000000002", "two")
	if _, err := replaceLocalGroupTemplate(filename, nil, first); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceLocalGroupTemplate(filename, nil, second); err != nil {
		t.Fatal(err)
	}
	next := cloneSavedWorkspace(first)
	next.Name = "two"
	before, _ := os.ReadFile(filename)
	if _, err := replaceLocalGroupTemplate(filename, &first, next); err == nil {
		t.Fatal("duplicate name was accepted")
	}
	after, _ := os.ReadFile(filename)
	if string(before) != string(after) {
		t.Fatal("failed update changed file")
	}
	if _, err := replaceLocalGroupTemplate(filename, &sampleWorkspaceMissing, next); err == nil {
		t.Fatal("missing source was recreated")
	}
}

func TestWorkspaceClonesPreserveEmptySlices(t *testing.T) {
	local := savedWorkspace{Entries: []workspaceTemplateEntry{}, RowPerc: []float32{}, ColumnPerc: []float32{}}
	cloud := cloudWorkspace{Entries: []cloudWorkspaceEntry{}, RowPerc: []float32{}, ColumnPerc: []float32{}}
	if !reflect.DeepEqual(local, cloneSavedWorkspace(local)) || !reflect.DeepEqual(cloud, cloneCloudWorkspace(cloud)) {
		t.Fatal("empty slices changed during snapshot cloning")
	}
}

var sampleWorkspaceMissing = sampleWorkspace("00000000000000000000000000000003", "missing")

func TestCloudWorkspaceReplacementRequiresExpectedVersion(t *testing.T) {
	first := cloudWorkspace{ID: "00000000000000000000000000000001", Name: "one", Entries: []cloudWorkspaceEntry{{Kind: "local", Label: "local"}}, SplitFirst: -1, SplitSecond: -1}
	second := cloudWorkspace{ID: "00000000000000000000000000000002", Name: "two", Entries: []cloudWorkspaceEntry{{Kind: "local", Label: "local"}}, SplitFirst: -1, SplitSecond: -1}
	state := cloudWorkspaceSnapshot{Revision: strings.Repeat("a", 64), Items: []cloudWorkspace{first, second}}
	next := cloneCloudWorkspace(first)
	next.ViewMode = "grid"
	updated, err := replaceCloudGroupSnapshot(state, &first, next)
	if err != nil || !reflect.DeepEqual(updated.Items[0], next) || !reflect.DeepEqual(state.Items[0], first) {
		t.Fatal("cloud update did not preserve immutable input", err)
	}
	stale := cloneCloudWorkspace(first)
	stale.Name = "external"
	if _, err = replaceCloudGroupSnapshot(state, &stale, next); err == nil {
		t.Fatal("stale cloud update was accepted")
	}
	collision := cloneCloudWorkspace(first)
	collision.Name = "two"
	if _, err = replaceCloudGroupSnapshot(state, &first, collision); err == nil {
		t.Fatal("cloud name collision was accepted")
	}
}

func TestPendingWorkspaceSyncHandlesLostResponseWithoutOverwritingChanges(t *testing.T) {
	base := cloudWorkspace{ID: "00000000000000000000000000000001", Name: "one", Entries: []cloudWorkspaceEntry{{Kind: "local", Label: "local"}}, SplitFirst: -1, SplitSecond: -1}
	attempt := cloneCloudWorkspace(base)
	attempt.ViewMode = "focus"
	next := cloneCloudWorkspace(base)
	next.ViewMode = "grid"
	p := &pendingWorkspaceSync{base: &base, attempted: &attempt, next: next}
	state := cloudWorkspaceSnapshot{Revision: strings.Repeat("a", 64), Items: []cloudWorkspace{attempt}}
	updated, already, err := preparePendingWorkspaceSync(state, p)
	if err != nil || already || !reflect.DeepEqual(updated.Items[0], next) {
		t.Fatal("lost-response retry failed", err)
	}
	state.Items[0] = next
	_, already, err = preparePendingWorkspaceSync(state, p)
	if err != nil || !already {
		t.Fatal("confirmed earlier write was not recognized", err)
	}
	external := cloneCloudWorkspace(base)
	external.Name = "changed elsewhere"
	state.Items[0] = external
	if _, _, err = preparePendingWorkspaceSync(state, p); err == nil {
		t.Fatal("retry overwrote an external update")
	}
	group := &terminalGroup{savedCloud: &base, pendingCloud: p}
	p.cloudOnly, p.signature = true, [32]byte{42}
	finishPendingWorkspaceSync(group, p)
	if group.pendingCloud != nil || !reflect.DeepEqual(*group.savedCloud, next) || group.savedSignature != p.signature {
		t.Fatal("successful cloud retry did not update the saved baseline")
	}
}
