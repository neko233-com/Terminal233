package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/sftp"
)

const maxTransferBytes = 16 << 20

type remoteFile struct {
	Name     string
	IsDir    bool
	IsLink   bool
	Size     int64
	Modified int64
}

func withSFTP(shell *remoteShell, f func(*sftp.Client) error) error {
	if shell == nil {
		return errors.New("SSH 尚未连接")
	}
	client, err := sftp.NewClient(shell.client)
	if err != nil {
		return err
	}
	defer client.Close()
	return f(client)
}
func listRemote(shell *remoteShell, dir string) ([]remoteFile, error) {
	var result []remoteFile
	err := withSFTP(shell, func(client *sftp.Client) error {
		files, err := client.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(files) > 5000 {
			return errors.New("目录条目过多，无法显示")
		}
		for _, f := range files {
			if f.Name() != "." && f.Name() != ".." && !strings.Contains(f.Name(), "/") {
				result = append(result, remoteFile{Name: f.Name(), IsDir: f.IsDir(), IsLink: f.Mode()&os.ModeSymlink != 0, Size: f.Size(), Modified: f.ModTime().Unix()})
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].IsDir != result[j].IsDir {
				return result[i].IsDir
			}
			return result[i].Name < result[j].Name
		})
		return nil
	})
	return result, err
}
func uploadFile(shell *remoteShell, local, dir string) error {
	file, err := os.Open(local)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxTransferBytes {
		return errors.New("只能上传不超过 16 MiB 的普通文件")
	}
	name := filepath.Base(local)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return errors.New("本地文件名无效")
	}
	remote := path.Join(dir, name)
	return withSFTP(shell, func(client *sftp.Client) error {
		dst, err := client.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(dst, io.LimitReader(file, maxTransferBytes+1))
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil || n > maxTransferBytes {
			_ = client.Remove(remote)
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			return errors.New("文件超过 16 MiB")
		}
		return nil
	})
}
func downloadFile(shell *remoteShell, remote, local string) error {
	return withSFTP(shell, func(client *sftp.Client) error {
		info, err := client.Lstat(remote)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxTransferBytes {
			return errors.New("只能下载不超过 16 MiB 的普通文件")
		}
		src, err := client.Open(remote)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(dst, io.LimitReader(src, maxTransferBytes+1))
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil || n > maxTransferBytes {
			_ = os.Remove(local)
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			return errors.New("文件超过 16 MiB")
		}
		return nil
	})
}
func deleteRemote(shell *remoteShell, remote string) error {
	if remote == "" || remote == "/" || remote == "." || remote == ".." {
		return errors.New("不能删除此路径")
	}
	return withSFTP(shell, func(client *sftp.Client) error {
		info, err := client.Lstat(remote)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return client.RemoveDirectory(remote)
		}
		if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return client.Remove(remote)
		}
		return fmt.Errorf("不支持删除此类型的文件")
	})
}
