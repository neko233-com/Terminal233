package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func eligibleBatchFile(f remoteFile) bool {
	return !f.IsDir && !f.IsLink && validDirectoryName(f.Name)
}

func (d *desktop) syncCheckedDirectory() {
	if d.fileCheckedDir != d.fileDir {
		d.fileChecked, d.fileCheckedDir = nil, d.fileDir
	}
}

func (d *desktop) checkedFileNames() ([]string, error) {
	if d.fileCheckedDir != d.fileDir {
		return nil, errors.New("目录已改变，请重新选择文件")
	}
	if len(d.fileChecked) == 0 || len(d.fileChecked) > maxBatchFiles {
		return nil, fmt.Errorf("请选择 1–%d 个普通文件", maxBatchFiles)
	}
	names := make([]string, 0, len(d.fileChecked))
	for _, file := range d.files {
		if d.fileChecked[file.Name] && eligibleBatchFile(file) {
			names = append(names, file.Name)
		}
	}
	if len(names) != len(d.fileChecked) {
		return nil, errors.New("文件列表已改变，请重新选择文件")
	}
	return names, nil
}

func (d *desktop) reconcileCheckedFiles() {
	d.syncCheckedDirectory()
	eligible := make(map[string]bool, len(d.files))
	for _, file := range d.files {
		if eligibleBatchFile(file) {
			eligible[file.Name] = true
		}
	}
	for name := range d.fileChecked {
		if !eligible[name] {
			delete(d.fileChecked, name)
		}
	}
}

func (d *desktop) selectVisibleBatchFiles(visible []remoteFile) {
	d.syncCheckedDirectory()
	checked := make(map[string]bool, len(d.fileChecked))
	for name := range d.fileChecked {
		checked[name] = true
	}
	for _, file := range visible {
		if eligibleBatchFile(file) {
			checked[file.Name] = true
		}
	}
	if len(checked) > maxBatchFiles {
		d.fileStatus = fmt.Sprintf("每次最多选择 %d 个文件", maxBatchFiles)
		return
	}
	d.fileChecked = checked
	d.fileStatus = fmt.Sprintf("已选择 %d 个文件", len(checked))
}

func downloadBatch(ctx context.Context, shell *remoteShell, dir, target string, names []string, progress func(int, int, string, int64)) (batchResult, error) {
	if strings.TrimSpace(dir) == "" || !filepath.IsAbs(target) || len(names) == 0 || len(names) > maxBatchFiles {
		return batchResult{}, errors.New("源目录、目标目录或文件数量无效")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !validDirectoryName(name) || seen[name] {
			return batchResult{}, errors.New("文件名无效或重复")
		}
		seen[name] = true
	}
	info, err := os.Stat(target)
	if err != nil {
		return batchResult{}, err
	}
	if !info.IsDir() {
		return batchResult{}, errors.New("目标不是目录")
	}
	return runBatchUpload(ctx, names, func(name string, progress func(int64)) error {
		destination, err := localItemPath(target, name)
		if err != nil {
			return err
		}
		if shell != nil {
			return downloadFileContext(ctx, shell, path.Join(dir, name), destination, progress)
		}
		source, err := localItemPath(dir, name)
		if err != nil {
			return err
		}
		return copyLocalFileContext(ctx, source, destination, progress)
	}, progress), nil
}

func (d *desktop) downloadCheckedFiles() {
	if d.busy || d.fileSessionUnavailable() {
		return
	}
	names, err := d.checkedFileNames()
	if err != nil {
		d.fileStatus = err.Error()
		return
	}
	target := d.fileBatchTarget
	if strings.TrimSpace(target) == "" || !filepath.IsAbs(target) {
		d.fileStatus = "请选择本机目标目录的绝对路径"
		return
	}
	shell, dir, sessionID := d.shell, d.fileDir, d.currentSession
	d.fileBatchErrors, d.fileBatchDetails = nil, false
	d.busy = true
	d.fileStatus = fmt.Sprintf("正在传输 0/%d…", len(names))
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelTransfer, d.transferSessionID = cancel, sessionID
	go func() {
		defer cancel()
		result, transferErr := downloadBatch(ctx, shell, dir, target, names, func(index, total int, name string, read int64) {
			d.win.Update(func() {
				if ctx.Err() != nil || d.cancelTransfer == nil || d.transferSessionID != sessionID {
					return
				}
				status := fmt.Sprintf("正在传输 %d/%d · %s · 已读取 %s", index, total, name, fileSizeLabel(remoteFile{Size: read}))
				if sessionID == d.currentSession && d.shell == shell && d.fileDir == dir {
					d.fileStatus = status
					return
				}
				for _, session := range d.sessions {
					if session.id == sessionID && session.shell == shell && session.fileDir == dir {
						session.fileStatus = status
						return
					}
				}
			})
		})
		if transferErr != nil {
			d.win.Update(func() { d.finishTransfer(sessionID, shell, dir, false, transferErr) })
			return
		}
		refresh := result.succeeded > 0 && transferUpdatesDirectory(shell, dir, filepath.Join(target, "placeholder"), false)
		d.win.Update(func() {
			d.setBatchFailures(sessionID, shell, result.errors)
			d.finishTransferStatus(sessionID, shell, dir, result.status(), refresh)
		})
	}()
}
