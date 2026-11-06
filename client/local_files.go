package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func localItemPath(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", errors.New("文件名无效")
	}
	return filepath.Join(dir, name), nil
}

func listLocal(dir string) ([]remoteFile, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(5001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 5000 {
		return nil, errors.New("目录条目过多，无法显示")
	}
	items := make([]remoteFile, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		items = append(items, remoteFile{Name: entry.Name(), IsDir: info.IsDir(), IsLink: info.Mode()&os.ModeSymlink != 0, Size: info.Size(), Modified: info.ModTime().Unix()})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func copyLocalFile(source, target string) error {
	return copyLocalFileContext(context.Background(), source, target, nil)
}
func copyLocalFileContext(ctx context.Context, source, target string, progress func(int64)) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("只能复制普通文件")
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	opened, err := src.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errors.New("源文件在打开时已改变")
	}
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	copyErr := copyTransfer(ctx, dst, src, progress)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		if removeErr := os.Remove(target); removeErr != nil {
			return fmt.Errorf("复制未完成，清理目标文件失败：%w", removeErr)
		}
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func deleteLocalItem(dir, name string) error {
	target, err := localItemPath(dir, name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("不支持删除此类型的文件")
	}
	// os.Remove removes a file, symlink, or empty directory; never recurse.
	return os.Remove(target)
}
