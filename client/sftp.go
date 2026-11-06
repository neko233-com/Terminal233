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
	if strings.TrimSpace(dir) == "" {
		return errors.New("请先填写当前目录")
	}
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

// renameFile stays within the selected directory and never requests the
// overwrite-capable OpenSSH posix-rename extension.
func renameFile(shell *remoteShell, dir, oldName, newName string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("请先填写当前目录")
	}
	if oldName == "" || oldName == "." || oldName == ".." || strings.ContainsAny(oldName, "/\\\x00") || !validDirectoryName(newName) {
		return errors.New("请输入单个有效文件名称")
	}
	if oldName == newName {
		return errors.New("新名称与原名称相同")
	}
	if shell == nil {
		return renameLocalNoReplace(filepath.Join(dir, oldName), filepath.Join(dir, newName))
	}
	return withSFTP(shell, func(client *sftp.Client) error {
		target := path.Join(dir, newName)
		if _, err := client.Lstat(target); err == nil {
			return &os.PathError{Op: "rename", Path: target, Err: os.ErrExist}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// SFTP v3 requires no overwrite, but some nonconforming servers can
		// race this check and replace a file. The UI warns about this limit.
		return client.Rename(path.Join(dir, oldName), target)
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

// An SFTP-only connection authenticates directly and verifies the subsystem
// without requesting a PTY or starting an interactive shell.
func openSFTPTransport(ctx context.Context, settings connectionSettings) (*remoteShell, error) {
	client, err := connectSSHClient(ctx, settings)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	probe, err := sftp.NewClient(client)
	if err == nil {
		err = probe.Close()
	}
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() { _ = client.Wait(); close(done) }()
	return &remoteShell{client: client, done: done}, nil
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
	before, err := os.Lstat(local)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("只能上传普通文件")
	}
	file, err := os.Open(local)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
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

// Copy between SSH sessions without a persistent local staging file. Exclusive
// creation protects an existing destination, and incomplete files are removed.
func copyRemoteFileContext(ctx context.Context, source, target *remoteShell, sourceDir, targetDir, name string, progress func(int64)) error {
	if source == nil || target == nil || strings.TrimSpace(sourceDir) == "" || strings.TrimSpace(targetDir) == "" || !validDirectoryName(name) {
		return errors.New("请选择有效的远程普通文件和目标目录")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sourcePath, targetPath := path.Join(sourceDir, name), path.Join(targetDir, name)
	return withSFTP(source, func(srcClient *sftp.Client) error {
		info, err := srcClient.Lstat(sourcePath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("只能传输普通文件")
		}
		src, err := srcClient.Open(sourcePath)
		if err != nil {
			return err
		}
		defer src.Close()
		return withSFTP(target, func(dstClient *sftp.Client) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			dst, err := dstClient.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
			if err != nil {
				return err
			}
			copyErr := copyTransfer(ctx, dst, src, progress)
			closeErr := dst.Close()
			if copyErr != nil || closeErr != nil {
				if removeErr := dstClient.Remove(targetPath); removeErr != nil {
					return fmt.Errorf("传输未完成，清理远程目标文件失败：%w", removeErr)
				}
				if copyErr != nil {
					return copyErr
				}
				return closeErr
			}
			return nil
		})
	})
}

// Either endpoint can be the local file system; two remote endpoints are
// streamed directly without a temporary local copy.
func copyPaneFileContext(ctx context.Context, source *remoteShell, sourceDir string, target *remoteShell, targetDir, name string, progress func(int64)) error {
	if source == nil && target == nil {
		return errors.New("请选择至少一个远程主机")
	}
	if !validDirectoryName(name) {
		return errors.New("文件名无效")
	}
	if source == nil {
		local, err := localItemPath(sourceDir, name)
		if err != nil {
			return err
		}
		return uploadFileContext(ctx, target, local, targetDir, progress)
	}
	if target == nil {
		local, err := localItemPath(targetDir, name)
		if err != nil {
			return err
		}
		return downloadFileContext(ctx, source, path.Join(sourceDir, name), local, progress)
	}
	return copyRemoteFileContext(ctx, source, target, sourceDir, targetDir, name, progress)
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
