package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

const maxDirectoryCopyEntries = 10000
const maxDirectoryCopyDepth = 32

func eligiblePaneItem(f remoteFile) bool {
	return validDirectoryName(f.Name) && !f.IsLink && (f.IsDir || eligibleBatchFile(f))
}

type paneFilesystem struct{ remote *sftp.Client }

func openPaneFilesystem(shell *remoteShell) (paneFilesystem, error) {
	if shell == nil {
		return paneFilesystem{}, nil
	}
	client, err := sftp.NewClient(shell.client)
	return paneFilesystem{remote: client}, err
}
func (f paneFilesystem) close() {
	if f.remote != nil {
		_ = f.remote.Close()
	}
}
func (f paneFilesystem) join(base, name string) string {
	if f.remote != nil {
		return path.Join(base, name)
	}
	return filepath.Join(base, name)
}
func (f paneFilesystem) stat(name string) (os.FileInfo, error) {
	if f.remote != nil {
		return f.remote.Lstat(name)
	}
	return os.Lstat(name)
}
func (f paneFilesystem) readDir(ctx context.Context, name string) ([]os.FileInfo, error) {
	if f.remote != nil {
		return f.remote.ReadDirContext(ctx, name)
	}
	dir, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxDirectoryCopyEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxDirectoryCopyEntries {
		return nil, errors.New("目录条目超过 10000 项")
	}
	infos := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}
func (f paneFilesystem) mkdir(name string) error {
	if f.remote != nil {
		return f.remote.Mkdir(name)
	}
	return os.Mkdir(name, 0700)
}
func (f paneFilesystem) openSource(name string) (io.ReadCloser, error) {
	info, err := f.stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("源文件已改变或不是普通文件")
	}
	if f.remote != nil {
		return f.remote.Open(name)
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, errors.New("源文件在打开时已改变")
	}
	return file, nil
}
func (f paneFilesystem) createExclusive(name string) (io.WriteCloser, error) {
	if f.remote != nil {
		return f.remote.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	}
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

type directoryCopyItem struct {
	parts     []string
	directory bool
}

type directoryCountingReader struct {
	io.Reader
	read int64
}

func (r *directoryCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	return n, err
}

func paneJoinParts(f paneFilesystem, base string, parts []string) string {
	for _, part := range parts {
		base = f.join(base, part)
	}
	return base
}

func verifyCreatedParents(dst paneFilesystem, targetRoot string, parts []string) error {
	if len(parts) == 0 {
		return nil
	}
	for i := 0; i < len(parts); i++ {
		dir := paneJoinParts(dst, targetRoot, parts[:i])
		info, err := dst.stat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("目标目录在复制期间已改变")
		}
	}
	return nil
}

// A bounded preflight rejects links and special files before creating any
// target. Completed entries remain if a later operation fails or is canceled;
// we do not recursively remove a directory that another process may modify.
func copyPaneEntryContext(ctx context.Context, source *remoteShell, sourceDir string, target *remoteShell, targetDir, name string, progress func(int64)) (partial bool, err error) {
	return copyPaneExpectedEntryContext(ctx, source, sourceDir, target, targetDir, name, nil, progress)
}

func copyPaneExpectedEntryContext(ctx context.Context, source *remoteShell, sourceDir string, target *remoteShell, targetDir, name string, expectedDirectory *bool, progress func(int64)) (partial bool, err error) {
	if !validDirectoryName(name) || strings.TrimSpace(sourceDir) == "" || strings.TrimSpace(targetDir) == "" {
		return false, errors.New("请选择有效的源和目标目录")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	src, err := openPaneFilesystem(source)
	if err != nil {
		return false, err
	}
	defer src.close()
	dst, err := openPaneFilesystem(target)
	if err != nil {
		return false, err
	}
	defer dst.close()
	sourceRoot, targetRoot := src.join(sourceDir, name), dst.join(targetDir, name)
	info, err := src.stat(sourceRoot)
	if err != nil {
		return false, err
	}
	if expectedDirectory != nil && info.IsDir() != *expectedDirectory {
		return false, errors.New("源文件类型已改变，请重新选择")
	}
	if info.Mode().IsRegular() {
		if source == nil && target == nil {
			return false, copyLocalFileContext(ctx, sourceRoot, targetRoot, progress)
		}
		return false, copyPaneFileContext(ctx, source, sourceDir, target, targetDir, name, progress)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("只能传输普通文件和目录")
	}
	if source == nil && target == nil {
		s, e1 := filepath.EvalSymlinks(sourceRoot)
		t, e2 := filepath.EvalSymlinks(targetDir)
		if e1 != nil {
			return false, e1
		}
		if e2 != nil {
			return false, e2
		}
		rel, e := filepath.Rel(s, t)
		if e == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return false, errors.New("不能复制到源目录内部")
		}
	} else if source != nil && target != nil && source.client.RemoteAddr().String() == target.client.RemoteAddr().String() {
		s, e1 := src.remote.RealPath(sourceRoot)
		t, e2 := dst.remote.RealPath(targetDir)
		if e1 != nil {
			return false, e1
		}
		if e2 != nil {
			return false, e2
		}
		s, t = path.Clean(s), path.Clean(t)
		if t == s || strings.HasPrefix(t, strings.TrimSuffix(s, "/")+"/") {
			return false, errors.New("不能复制到源目录内部")
		}
	}
	manifest := []directoryCopyItem{{directory: true}}
	var scan func([]string, int) error
	scan = func(parts []string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > maxDirectoryCopyDepth {
			return errors.New("目录层级超过 32 层")
		}
		dir := paneJoinParts(src, sourceRoot, parts)
		current, err := src.stat(dir)
		if err != nil {
			return err
		}
		if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
			return errors.New("源目录已改变或包含符号链接")
		}
		entries, err := src.readDir(ctx, dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validDirectoryName(entry.Name()) {
				return errors.New("源目录包含不支持的文件名")
			}
			child := append(append([]string(nil), parts...), entry.Name())
			actual, err := src.stat(paneJoinParts(src, sourceRoot, child))
			if err != nil {
				return err
			}
			if actual.Mode()&os.ModeSymlink != 0 || (!actual.IsDir() && !actual.Mode().IsRegular()) {
				return errors.New("源目录包含符号链接或特殊文件")
			}
			if len(manifest) >= maxDirectoryCopyEntries {
				return errors.New("目录条目超过 10000 项")
			}
			manifest = append(manifest, directoryCopyItem{parts: child, directory: actual.IsDir()})
			if actual.IsDir() {
				if err := scan(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := scan(nil, 0); err != nil {
		return false, err
	}
	var total int64
	for _, item := range manifest {
		if err := ctx.Err(); err != nil {
			return partial, err
		}
		sourcePath, targetPath := paneJoinParts(src, sourceRoot, item.parts), paneJoinParts(dst, targetRoot, item.parts)
		if err := verifyCreatedParents(dst, targetRoot, item.parts); err != nil {
			return partial, err
		}
		if item.directory {
			current, err := src.stat(sourcePath)
			if err != nil {
				return partial, err
			}
			if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
				return partial, errors.New("源目录在复制期间已改变")
			}
			if err := dst.mkdir(targetPath); err != nil {
				return partial, err
			}
			partial = true
			continue
		}
		file, err := src.openSource(sourcePath)
		if err != nil {
			return partial, err
		}
		out, err := dst.createExclusive(targetPath)
		if err != nil {
			_ = file.Close()
			return partial, err
		}
		base := total
		reader := &directoryCountingReader{Reader: file}
		copyErr := copyTransfer(ctx, out, reader, func(n int64) {
			if progress != nil {
				progress(base + n)
			}
		})
		total = base + reader.read
		closeErr, sourceCloseErr := out.Close(), file.Close()
		if copyErr != nil {
			return partial, copyErr
		}
		if closeErr != nil {
			return partial, closeErr
		}
		if sourceCloseErr != nil {
			return partial, fmt.Errorf("关闭源文件失败：%w", sourceCloseErr)
		}
	}
	return false, nil
}
