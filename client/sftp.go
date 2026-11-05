package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/sftp"
)

type contextReader struct {
	ctx      context.Context
	r        io.Reader
	progress func(int64)
	read     int64
	last     time.Time
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n > 0 && r.progress != nil {
		r.read += int64(n)
		now := time.Now()
		if r.last.IsZero() || now.Sub(r.last) >= 250*time.Millisecond {
			r.last = now
			r.progress(r.read)
		}
	}
	return n, err
}

func copyTransfer(ctx context.Context, dst io.Writer, src io.Reader, progress func(int64)) error {
	_, err := io.Copy(dst, &contextReader{ctx: ctx, r: src, progress: progress})
	if err == nil {
		err = ctx.Err()
	}
	return err
}

type remoteFile struct {
	Name     string
	IsDir    bool
	IsLink   bool
	Size     int64
	Modified int64
}

func validDirectoryName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\:\x00\r\n") || !filepath.IsLocal(name) {
		return false
	}
	return strings.TrimSpace(name) == name && strings.IndexFunc(name, unicode.IsControl) == -1
}

func makeDirectory(shell *remoteShell, dir, name string) error {
	if !validDirectoryName(name) {
		return errors.New("请输入单个有效目录名称")
	}
	if shell == nil {
		return os.Mkdir(filepath.Join(dir, name), 0700)
	}
	return withSFTP(shell, func(client *sftp.Client) error {
		return client.Mkdir(path.Join(dir, name))
	})
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
	return uploadFileContext(context.Background(), shell, local, dir, nil)
}
func uploadFileContext(ctx context.Context, shell *remoteShell, local, dir string, progress func(int64)) error {
	file, err := os.Open(local)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("只能上传普通文件")
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
		copyErr := copyTransfer(ctx, dst, file, progress)
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil {
			if removeErr := client.Remove(remote); removeErr != nil {
				return fmt.Errorf("传输未完成，清理远程目标文件失败：%w", removeErr)
			}
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
}
func downloadFile(shell *remoteShell, remote, local string) error {
	return downloadFileContext(context.Background(), shell, remote, local, nil)
}
func downloadFileContext(ctx context.Context, shell *remoteShell, remote, local string, progress func(int64)) error {
	return withSFTP(shell, func(client *sftp.Client) error {
		info, err := client.Lstat(remote)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("只能下载普通文件")
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
		copyErr := copyTransfer(ctx, dst, src, progress)
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil {
			if removeErr := os.Remove(local); removeErr != nil {
				return fmt.Errorf("传输未完成，清理本机目标文件失败：%w", removeErr)
			}
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
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
