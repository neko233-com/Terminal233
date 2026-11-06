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

func TestBatchLocalPartialSuccessDoesNotOverwrite(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	for _, name := range []string{"one.txt", "exists.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(destination, "exists.txt"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(source, "one.txt"), filepath.Join(source, "exists.txt"), filepath.Join(source, "two.txt")}
	result := runBatchUpload(context.Background(), files, func(local string, progress func(int64)) error {
		target, err := localItemPath(destination, filepath.Base(local))
		if err != nil {
			return err
		}
		return copyLocalFileContext(context.Background(), local, target, progress)
	}, nil)
	if result.succeeded != 2 || result.failed != 1 || result.skipped != 0 || !strings.Contains(result.status(), "exists.txt") {
		t.Fatalf("unexpected batch result: %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "exists.txt")); err != nil || string(data) != "protected" {
		t.Fatal("batch overwrote existing file")
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if data, err := os.ReadFile(filepath.Join(destination, name)); err != nil || string(data) != name {
			t.Fatalf("missing copied file %s", name)
		}
	}
}

func TestBatchCancellationCleansPartialAndSkipsRemaining(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	a, b := filepath.Join(source, "a"), filepath.Join(source, "b")
	if err := os.WriteFile(a, make([]byte, 128*1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := runBatchUpload(ctx, []string{a, b}, func(local string, progress func(int64)) error {
		return copyLocalFileContext(ctx, local, filepath.Join(destination, filepath.Base(local)), progress)
	}, func(_, _ int, _ string, _ int64) { cancel() })
	if !result.cancelled || result.succeeded != 0 || result.failed != 0 || result.skipped != 1 {
		t.Fatalf("unexpected cancellation: %+v", result)
	}
	if result.cancelledFile != "a" || !strings.Contains(result.status(), "已取消当前文件：a") {
		t.Fatal("cancelled in-flight file was not identified")
	}
	if _, err := os.Stat(filepath.Join(destination, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial file was not cleaned")
	}
	if _, err := os.Stat(filepath.Join(destination, "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unprocessed file was created")
	}
}

func TestBatchFailuresRemainAccessibleInTheirSession(t *testing.T) {
	files := []string{"one", "two", "three"}
	result := runBatchUpload(context.Background(), files, func(name string, _ func(int64)) error {
		if name != "two" {
			return errors.New("denied")
		}
		return nil
	}, nil)
	if result.succeeded != 1 || result.failed != 2 || len(result.errors) != 2 || !strings.Contains(result.errors[0], "one") || !strings.Contains(result.errors[1], "three") {
		t.Fatalf("missing per-file failures: %+v", result)
	}
	d := &desktop{workspace: "files", currentSession: 2, sessions: []*workspaceSession{{id: 1}, {id: 2}}}
	d.setBatchFailures(1, nil, result.errors)
	if len(d.fileBatchErrors) != 0 || len(d.sessions[0].fileBatchErrors) != 2 {
		t.Fatal("background failures appeared in the wrong session")
	}
	d.activateSession(1)
	d.workspace = "files"
	view := ui.NewTester(d.view, 1200, 1500)
	if err := view.Click("查看最近批量传输失败详情（2）"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("one：denied") || !view.HasText("three：denied") {
		t.Fatal("not all failed files were visible")
	}
	d.activateSession(2)
	d.activateSession(1)
	if !d.fileBatchDetails || len(d.fileBatchErrors) != 2 {
		t.Fatal("failure details were lost when switching sessions")
	}
}

func TestBatchSFTPContinuesAfterCollision(t *testing.T) {
	settings, fp, _ := testSSHServer(t)
	settings.Fingerprint = fp
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	source, destination := t.TempDir(), t.TempDir()
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(destination, "one"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result := runBatchUpload(ctx, []string{filepath.Join(source, "one"), filepath.Join(source, "two")}, func(local string, progress func(int64)) error {
		return uploadFileContext(ctx, shell, local, filepath.ToSlash(destination), progress)
	}, nil)
	if result.succeeded != 1 || result.failed != 1 {
		t.Fatalf("unexpected SFTP result: %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "one")); err != nil || string(data) != "protected" {
		t.Fatal("SFTP overwrite")
	}
	if data, err := os.ReadFile(filepath.Join(destination, "two")); err != nil || string(data) != "two" {
		t.Fatal("SFTP did not continue")
	}
}

func TestBatchBackgroundPartialResultKeepsSessionAndRefresh(t *testing.T) {
	d := &desktop{busy: true, currentSession: 2, sessions: []*workspaceSession{{id: 1, fileDir: "/one"}, {id: 2, fileDir: "/two"}}, fileDir: "/two"}
	d.finishTransferStatus(1, nil, "/one", "批量传输完成：成功 1，失败 1，未处理 0", true)
	if d.busy || !d.sessions[0].refreshPending || !strings.Contains(d.sessions[0].fileStatus, "失败 1") || d.fileDir != "/two" {
		t.Fatal("partial batch result lost across sessions")
	}
}

func TestBatchDropLimitIsReportedBeforeTransfer(t *testing.T) {
	d := &desktop{workspace: "files", term: &terminal.Terminal{}, fileDir: t.TempDir()}
	view := ui.NewTester(d.view, 1200, 1400)
	zone, ok := view.Find("会话文件面板")
	if !ok {
		t.Fatal("batch drop zone missing")
	}
	files := make([]string, maxBatchFiles+1)
	for i := range files {
		files[i] = "file"
	}
	if !view.DropFiles(zone.X+zone.W/2, zone.Y+zone.H/2, files...) {
		t.Fatal("drop not handled")
	}
	if !strings.Contains(d.fileStatus, "最多传输 100") || d.busy {
		t.Fatal("batch limit did not stop transfer")
	}
}
