package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyBetweenSSHHostsProtectsDestinationAndCleansCancellation(t *testing.T) {
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
	srcDir, dstDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("remote-copy-test"), 20000)
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	from, to := filepath.ToSlash(srcDir), filepath.ToSlash(dstDir)
	if err := copyRemoteFileContext(context.Background(), source, target, from, to, "file.txt", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "file.txt"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("remote copy did not preserve contents")
	}
	if err := copyRemoteFileContext(context.Background(), source, target, from, to, "file.txt", nil); err == nil {
		t.Fatal("existing target was overwritten")
	}
	got, _ = os.ReadFile(filepath.Join(dstDir, "file.txt"))
	if !bytes.Equal(got, data) {
		t.Fatal("existing target changed")
	}
	if err := copyRemoteFileContext(context.Background(), source, target, from, to, "../file.txt", nil); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.Mkdir(filepath.Join(srcDir, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyRemoteFileContext(context.Background(), source, target, from, to, "dir", nil); err == nil {
		t.Fatal("accepted a directory as a file")
	}
	if err := os.WriteFile(filepath.Join(srcDir, "cancel.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = copyRemoteFileContext(ctx, source, target, from, to, "cancel.txt", func(int64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "cancel.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled copy left a partial target")
	}
	localSource, localTarget := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(localSource, "upload.txt"), []byte("local upload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyPaneFileContext(context.Background(), nil, localSource, target, to, "upload.txt", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dstDir, "upload.txt")); err != nil || string(got) != "local upload" {
		t.Fatal("local-to-remote copy failed")
	}
	link := filepath.Join(localSource, "link.txt")
	if err := os.Symlink(filepath.Join(localSource, "upload.txt"), link); err == nil {
		if err := copyPaneFileContext(context.Background(), nil, localSource, target, to, "link.txt", nil); err == nil {
			t.Fatal("symlink source was accepted")
		}
	}
	if err := copyPaneFileContext(context.Background(), source, from, nil, localTarget, "file.txt", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(localTarget, "file.txt")); err != nil || !bytes.Equal(got, data) {
		t.Fatal("remote-to-local copy failed")
	}
	if err := copyPaneFileContext(context.Background(), source, from, nil, localTarget, "file.txt", nil); err == nil {
		t.Fatal("local target was overwritten")
	}
}
