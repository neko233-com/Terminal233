package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryCopyAcrossLocalAndSSHFilesystems(t *testing.T) {
	a, af, _ := testSSHServer(t)
	a.Fingerprint = af
	b, bf, _ := testSSHServer(t)
	b.Fingerprint = bf
	first, err := connectShell(a)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := connectShell(b)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "project", "nested", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	contents := bytes.Repeat([]byte("folder-test"), 10000)
	if err := os.WriteFile(filepath.Join(source, "project", "nested", "data.txt"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if partial, err := copyPaneEntryContext(context.Background(), first, filepath.ToSlash(source), first, filepath.ToSlash(filepath.Join(source, "project", "nested")), "project", nil); err == nil || partial {
		t.Fatal("same-server copy into the source tree was allowed")
	}
	for _, tc := range []struct {
		name     string
		src, dst *remoteShell
	}{{"local-local", nil, nil}, {"local-remote", nil, first}, {"remote-local", first, nil}, {"remote-remote", first, second}} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(root, tc.name)
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			from, to := source, target
			if tc.src != nil {
				from = filepath.ToSlash(from)
			}
			if tc.dst != nil {
				to = filepath.ToSlash(to)
			}
			partial, err := copyPaneEntryContext(context.Background(), tc.src, from, tc.dst, to, "project", nil)
			if err != nil || partial {
				t.Fatalf("copy failed: partial=%v %v", partial, err)
			}
			got, err := os.ReadFile(filepath.Join(target, "project", "nested", "data.txt"))
			if err != nil || !bytes.Equal(got, contents) {
				t.Fatal("copied file differs")
			}
			if info, err := os.Stat(filepath.Join(target, "project", "nested", "empty")); err != nil || !info.IsDir() {
				t.Fatal("empty directory was lost")
			}
			partial, err = copyPaneEntryContext(context.Background(), tc.src, from, tc.dst, to, "project", nil)
			if err == nil || partial {
				t.Fatal("existing destination was not protected")
			}
			got, _ = os.ReadFile(filepath.Join(target, "project", "nested", "data.txt"))
			if !bytes.Equal(got, contents) {
				t.Fatal("existing content changed")
			}
		})
	}
}

func TestDirectoryCopyRejectsUnsafeTreesAndRetainsPartialOnCancel(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(src, "project", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("copy-me"), 100000)
	if err := os.WriteFile(filepath.Join(src, "project", "nested", "large.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if partial, err := copyPaneEntryContext(context.Background(), nil, src, nil, filepath.Join(src, "project", "nested"), "project", nil); err == nil || partial {
		t.Fatal("copying into the source tree was allowed")
	}
	if partial, err := copyPaneEntryContext(context.Background(), nil, src, nil, dst, "../project", nil); err == nil || partial {
		t.Fatal("path traversal was allowed")
	}
	link := filepath.Join(src, "project", "link")
	if err := os.Symlink(filepath.Join(src, "project", "nested", "large.txt"), link); err == nil {
		if partial, err := copyPaneEntryContext(context.Background(), nil, src, nil, dst, "project", nil); err == nil || partial {
			t.Fatal("symlink tree was accepted or target created")
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	partial, err := copyPaneEntryContext(ctx, nil, src, nil, dst, "project", func(int64) { cancel() })
	if !partial || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel did not report retained partial directory: partial=%v err=%v", partial, err)
	}
	if info, err := os.Stat(filepath.Join(dst, "project")); err != nil || !info.IsDir() {
		t.Fatal("partial target was unexpectedly removed")
	}
	got, err := os.ReadFile(filepath.Join(src, "project", "nested", "large.txt"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("source changed during canceled copy")
	}
}

func TestDirectoryCopyRejectsReplacedTargetParents(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyCreatedParents(paneFilesystem{}, root, []string{"nested", "file"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
		t.Skip("symbolic links unavailable")
	}
	if err := verifyCreatedParents(paneFilesystem{}, root, []string{"nested", "file"}); err == nil {
		t.Fatal("replaced parent symlink was accepted")
	}
}
