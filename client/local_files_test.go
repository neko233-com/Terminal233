package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransferProgressAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := bytes.Repeat([]byte("a"), 128*1024)
	var dst bytes.Buffer
	var reported int64
	err := copyTransfer(ctx, &dst, bytes.NewReader(data), func(read int64) {
		reported = read
		cancel()
	})
	if !errors.Is(err, context.Canceled) || reported <= 0 || reported >= int64(len(data)) {
		t.Fatalf("progress/cancellation failed: bytes=%d err=%v", reported, err)
	}
}

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
	if err := copyLocalFileContext(ctx, source, target, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy returned %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target remains: %v", err)
	}
}

func TestCreateLocalDirectoryRejectsTraversalAndExistingEntries(t *testing.T) {
	root := t.TempDir()
	if err := makeDirectory(nil, "", "safe-name"); err == nil {
		t.Fatal("empty current directory was accepted")
	}
	for _, name := range []string{"", ".", "..", "../outside", "a/b", `a\b`, "a:b", "bad\tname", " name "} {
		if err := makeDirectory(nil, root, name); err == nil {
			t.Fatalf("accepted directory name %q", name)
		}
	}
	if err := makeDirectory(nil, root, "新目录"); err != nil {
		t.Fatal(err)
	}
	if err := makeDirectory(nil, root, "新目录"); err == nil {
		t.Fatal("existing directory was replaced")
	}
	if info, err := os.Stat(filepath.Join(root, "新目录")); err != nil || !info.IsDir() {
		t.Fatal("directory was not created")
	}
}

func TestRenameLocalDoesNotOverwriteOrEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{"existing", "../escape", "a/b", ""} {
		if err := renameFile(nil, root, "source", dest); err == nil {
			t.Fatalf("accepted target %q", dest)
		}
	}
	if err := renameFile(nil, root, "../source", "safe"); err == nil {
		t.Fatal("source traversal accepted")
	}
	if err := renameFile(nil, root, "source", "renamed"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "renamed"))
	if err != nil || string(data) != "source" {
		t.Fatalf("source lost: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "existing"))
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing target changed: %q %v", data, err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := renameFile(nil, root, "dir", "new-dir"); err != nil {
		t.Fatal(err)
	}
}
