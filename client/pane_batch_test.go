package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaneBatchCopiesAcrossSSHAndReportsConflicts(t *testing.T) {
	a, af, _ := testSSHServer(t)
	a.Fingerprint = af
	b, bf, _ := testSSHServer(t)
	b.Fingerprint = bf
	source, err := connectShell(a)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := connectShell(b)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "one.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "conflict.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "conflict.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(from, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "folder", "inside"), []byte("directory"), 0600); err != nil {
		t.Fatal(err)
	}
	items := []paneCopyItem{{name: "one.txt"}, {name: "conflict.txt"}, {name: "folder", directory: true}}
	result := runPaneBatch(context.Background(), items, func(item paneCopyItem, report func(int64)) (bool, error) {
		return copyPaneExpectedEntryContext(context.Background(), source, filepath.ToSlash(from), target, filepath.ToSlash(to), item.name, &item.directory, report)
	}, nil)
	if result.succeeded != 2 || result.failed != 1 || result.skipped != 0 || result.partial != 0 || len(result.errors) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(to, "conflict.txt")); string(got) != "existing" {
		t.Fatal("existing file was overwritten")
	}
	if got, err := os.ReadFile(filepath.Join(to, "folder", "inside")); err != nil || string(got) != "directory" {
		t.Fatal("directory did not copy after another item failed")
	}
	if !strings.Contains(result.status(), "成功 2，失败 1") {
		t.Fatal("summary does not describe partial failure")
	}
}

func TestPaneBatchCancellationReportsRetainedPartialDirectory(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(from, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "folder", "large"), bytes.Repeat([]byte("x"), 200000), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "later"), []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	items := []paneCopyItem{{name: "folder", directory: true}, {name: "later"}}
	result := runPaneBatch(ctx, items, func(item paneCopyItem, report func(int64)) (bool, error) {
		return copyPaneExpectedEntryContext(ctx, nil, from, nil, to, item.name, &item.directory, report)
	}, func(int, int, string, int64) { cancel() })
	if !result.cancelled || result.partial != 1 || result.skipped != 1 || !strings.Contains(result.status(), "保留部分内容") {
		t.Fatalf("cancel result incorrect: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(to, "folder")); err != nil {
		t.Fatal("partial folder was removed")
	}
	if _, err := os.Stat(filepath.Join(to, "later")); !os.IsNotExist(err) {
		t.Fatal("batch continued after cancellation")
	}
}
