package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFilesBrowseCopyAndDeleteSafely(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "folder")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	items, err := listLocal(root)
	if err != nil || len(items) != 2 || !items[0].IsDir || items[1].Name != "source.txt" || items[1].Size != 5 {
		t.Fatalf("unexpected local listing: %+v %v", items, err)
	}
	target := filepath.Join(dir, "copy.txt")
	if err := copyLocalFile(source, target); err != nil {
		t.Fatal(err)
	}
	if err := copyLocalFile(source, target); err == nil {
		t.Fatal("copy overwrote an existing file")
	}
	if err := deleteLocalItem(root, "folder"); err == nil {
		t.Fatal("non-empty directory was deleted")
	}
	if err := deleteLocalItem(root, "../outside"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := deleteLocalItem(dir, "copy.txt"); err != nil {
		t.Fatal(err)
	}
	if err := deleteLocalItem(root, "folder"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "hello" {
		t.Fatalf("source changed: %q %v", data, err)
	}
}

func TestCancelledLocalCopyRemovesPartialTarget(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyLocalFileContext(ctx, source, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy returned %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target remains: %v", err)
	}
}
