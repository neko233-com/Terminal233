package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestBatchDownloadSFTPDoesNotOverwriteAndContinues(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	source, target := t.TempDir(), t.TempDir()
	for _, name := range []string{"one.txt", "exists.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "exists.txt"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := downloadBatch(context.Background(), shell, filepath.ToSlash(source), target, []string{"one.txt", "exists.txt", "two.txt"}, nil)
	if err != nil || result.succeeded != 2 || result.failed != 1 || len(result.errors) != 1 {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if data, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(data) != name {
			t.Fatalf("download failed for %s", name)
		}
	}
	if data, err := os.ReadFile(filepath.Join(target, "exists.txt")); err != nil || string(data) != "protected" {
		t.Fatal("existing target was overwritten")
	}
	for _, names := range [][]string{{"../escape"}, {"one.txt", "one.txt"}, {"dir/file"}, {"bad:name"}} {
		if _, err := downloadBatch(context.Background(), shell, filepath.ToSlash(source), target, names, nil); err == nil {
			t.Fatalf("unsafe names accepted: %q", names)
		}
	}
}

func TestBatchDownloadCancellationCleansPartialLocalCopy(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "large"), make([]byte, 128*1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "later"), []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := downloadBatch(ctx, nil, source, target, []string{"large", "later"}, func(_, _ int, _ string, _ int64) { cancel() })
	if err != nil || !result.cancelled || result.skipped != 1 {
		t.Fatalf("unexpected cancellation result: %+v %v", result, err)
	}
	for _, name := range []string{"large", "later"} {
		if _, err := os.Stat(filepath.Join(target, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial or skipped file remains: %s", name)
		}
	}
}

func TestBatchDownloadSelectionIsScopedAndBounded(t *testing.T) {
	term := &terminal.Terminal{}
	d := &desktop{workspace: "files", term: term, currentSession: 1, fileDir: "/one", sessions: []*workspaceSession{{id: 1, term: term}, {id: 2, fileDir: "/two"}}, files: []remoteFile{{Name: "one"}, {Name: "two"}, {Name: "folder", IsDir: true}, {Name: "link", IsLink: true}}}
	view := ui.NewTester(d.view, 1200, 1400)
	if err := view.Click("多选文件：one"); err != nil {
		t.Fatal(err)
	}
	if !d.fileChecked["one"] || len(d.fileChecked) != 1 {
		t.Fatal("file checkbox did not select the file")
	}
	if err := view.Click("选择可见文件"); err != nil {
		t.Fatal(err)
	}
	if len(d.fileChecked) != 2 || d.fileChecked["folder"] || d.fileChecked["link"] {
		t.Fatal("selection included non-regular entries")
	}
	if names, err := d.checkedFileNames(); err != nil || strings.Join(names, ",") != "one,two" {
		t.Fatalf("unexpected selected names: %q %v", names, err)
	}
	d.fileBatchTarget = "target-one"
	d.activateSession(2)
	if len(d.fileChecked) != 0 || d.fileBatchTarget != "" {
		t.Fatal("selection leaked into another session")
	}
	d.activateSession(1)
	if len(d.fileChecked) != 2 || d.fileBatchTarget != "target-one" {
		t.Fatal("selection was not restored with its session")
	}
	d.fileDir = "/changed"
	if _, err := d.checkedFileNames(); err == nil {
		t.Fatal("old directory selection was accepted")
	}
	d.syncCheckedDirectory()
	if len(d.fileChecked) != 0 {
		t.Fatal("directory change retained old selection")
	}
	visible := make([]remoteFile, maxBatchFiles+1)
	for i := range visible {
		visible[i].Name = strings.Repeat("a", i+1)
	}
	d.selectVisibleBatchFiles(visible)
	if len(d.fileChecked) != 0 || !strings.Contains(d.fileStatus, "最多选择") {
		t.Fatal("oversized selection was accepted")
	}
}
