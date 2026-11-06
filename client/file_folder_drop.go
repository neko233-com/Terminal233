package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

type paneFolderTarget struct {
	shell       *remoteShell
	parent, dir string
	right       bool
}

// The build overlay preserves these values at drag start. Other MyGo drag
// types retain the framework's normal per-frame update behavior.
func (localPaneDrag) NekoFreezeDragValue()     {}
func (remotePaneDrag) NekoFreezeDragValue()    {}
func (secondaryPaneDrag) NekoFreezeDragValue() {}

// Resolve from the displayed directory, never from an uncommitted path input.
// A blank name describes the pane itself; a name describes one of its folders.
func (d *desktop) paneFolderTarget(right bool, name string) (paneFolderTarget, bool) {
	target := paneFolderTarget{right: right}
	if d.busy || d.win == nil {
		return target, false
	}
	var files []remoteFile
	if right {
		if d.fileSessionUnavailable() || !d.rightDirectoryReady() {
			return target, false
		}
		target.shell, target.parent, files = d.shell, d.fileDir, d.files
	} else if d.secondaryPane.sessionID != 0 {
		p := &d.secondaryPane
		if d.secondarySession() == nil || p.loading || p.input != p.directory || !paneHistoryAt(p.history, p.directory) {
			return target, false
		}
		target.shell, target.parent, files = p.shell, p.directory, p.files
	} else {
		p := &d.localPane
		if p.loading || !sameLocalDirectory(p.input, p.directory) || !paneHistoryAt(p.history, p.directory) {
			return target, false
		}
		target.parent, files = p.directory, p.files
	}
	if strings.TrimSpace(target.parent) == "" {
		return target, false
	}
	target.dir = target.parent
	if name == "" {
		return target, true
	}
	for _, file := range files {
		if file.Name == name && file.IsDir && eligiblePaneItem(file) {
			if target.shell == nil {
				target.dir = filepath.Join(target.parent, name)
			} else {
				target.dir = path.Join(target.parent, name)
			}
			return target, true
		}
	}
	return target, false
}

func paneHistoryAt(history fileHistory, dir string) bool {
	return len(history.paths) > 0 && history.index >= 0 && history.index < len(history.paths) && history.paths[history.index] == dir
}

func (d *desktop) folderDropSource(target paneFolderTarget, value any) (*remoteShell, string, []paneCopyItem, bool) {
	var shell *remoteShell
	var dir string
	var items []paneCopyItem
	valid := false
	switch item := value.(type) {
	case localPaneDrag:
		p := &d.localPane
		dir, items = item.directory, item.items
		valid = d.secondaryPane.sessionID == 0 && !p.loading && item.generation == p.generation && sameLocalDirectory(item.directory, p.directory) && sameLocalDirectory(p.input, p.directory) && paneItemsInclude(items, item.name) && paneItemsMatch(items, p.files)
		if target.right {
			valid = valid && d.canUploadPaneFile(item)
		}
	case remotePaneDrag:
		shell, dir, items = item.shell, item.directory, item.items
		valid = !d.fileSessionUnavailable() && d.rightDirectoryReady() && item.sessionID == d.currentSession && item.shell == d.shell && item.directory == d.fileDir && paneItemsInclude(items, item.name) && paneItemsMatch(items, d.files)
		if !target.right {
			if d.secondaryPane.sessionID != 0 {
				valid = valid && d.canCopyRightToSecondary(item)
			} else {
				_, err := d.paneDownloadTarget(item)
				valid = valid && err == nil && item.secondaryID == 0 && item.localGeneration == d.localPane.generation && sameLocalDirectory(item.localDirectory, d.localPane.directory)
			}
		}
	case secondaryPaneDrag:
		p := &d.secondaryPane
		shell, dir, items = item.shell, item.directory, item.items
		valid = d.secondarySession() != nil && !p.loading && p.input == p.directory && paneHistoryAt(p.history, p.directory) && item.sessionID == p.sessionID && item.shell == p.shell && item.directory == p.directory && item.generation == p.generation && paneItemsInclude(items, item.name) && paneItemsMatch(items, p.files)
		if target.right {
			valid = valid && d.canCopySecondaryToRight(item)
		}
	}
	// Refuse dragging a selection containing the destination folder into itself.
	// The copier also resolves paths to reject deeper self-descendant targets.
	if valid && shell == target.shell && (dir == target.parent || (shell == nil && sameLocalDirectory(dir, target.parent))) {
		for _, item := range items {
			if item.directory && ((shell != nil && path.Join(dir, item.name) == target.dir) || (shell == nil && sameLocalDirectory(filepath.Join(dir, item.name), target.dir))) {
				return nil, "", nil, false
			}
		}
	}
	return shell, dir, items, valid
}

// Folder rows are the innermost native and in-app drop targets. Dropping
// copies into the folder without navigating or altering the source selection.
func (d *desktop) fileFolderDrop(c *ui.Context, row ui.Element, right bool, file remoteFile) bool {
	if !file.IsDir || !eligiblePaneItem(file) {
		return false
	}
	target, ready := d.paneFolderTarget(right, file.Name)
	if !ready {
		return false
	}
	if value, over := ui.DragOver[any](row); over {
		if _, _, _, valid := d.folderDropSource(target, value); valid {
			row.Background(c.Theme().SurfaceHover).Tooltip("复制到 " + file.Name)
		}
	}
	if row.FileDragOver() {
		row.Background(c.Theme().SurfaceHover).Tooltip("复制到 " + file.Name)
	}
	if files := row.DroppedFiles(); len(files) > 0 {
		d.copyExternalFilesToFolder(files, target)
		return true
	}
	if value, dropped := ui.Drop[any](row); dropped {
		shell, dir, items, valid := d.folderDropSource(target, value)
		if valid {
			d.startPaneCopy(shell, dir, target.shell, target.dir, items, target.right)
		} else {
			d.folderDropStatus(target, "源或目标已改变，或目标包含在所选目录中；请重新拖拽")
		}
		return true
	}
	return false
}

func (d *desktop) folderDropStatus(target paneFolderTarget, status string) {
	d.fileStatus = status
	if !target.right {
		if d.secondaryPane.sessionID == 0 {
			d.localPane.status = status
		} else {
			d.secondaryPane.status = status
		}
	}
}

func (d *desktop) copyExternalFilesToFolder(files []string, target paneFolderTarget) {
	items, directories, err := externalPaneCopySources(files, target.shell == nil)
	if err != nil {
		d.folderDropStatus(target, err.Error())
		return
	}
	d.startPaneCopySources(nil, "", target.shell, target.dir, items, target.right, directories)
}

// Explorer can supply files from different source directories. Validate the
// entire batch before creating anything and reject ambiguous target names.
func externalPaneCopySources(files []string, localTarget bool) ([]paneCopyItem, map[string]string, error) {
	if len(files) == 0 || len(files) > maxBatchFiles {
		return nil, nil, fmt.Errorf("每次最多传输 %d 项", maxBatchFiles)
	}
	items := make([]paneCopyItem, 0, len(files))
	directories := make(map[string]string, len(files))
	seen := make(map[string]bool, len(files))
	for _, source := range files {
		if !filepath.IsAbs(source) {
			return nil, nil, errors.New("拖入的文件必须使用本机绝对路径")
		}
		source = filepath.Clean(source)
		name := filepath.Base(source)
		if !validDirectoryName(name) {
			return nil, nil, errors.New("拖入的文件名不能用于目标目录")
		}
		key := name
		if localTarget && os.PathSeparator == '\\' {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return nil, nil, errors.New("拖入的条目包含同名目标，请分批复制")
		}
		info, err := os.Lstat(source)
		if err != nil {
			return nil, nil, fmt.Errorf("无法读取 %s：%w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return nil, nil, errors.New("只能拖入普通文件和目录，不支持链接或特殊文件")
		}
		seen[key], directories[name] = true, filepath.Dir(source)
		items = append(items, paneCopyItem{name: name, directory: info.IsDir()})
	}
	return items, directories, nil
}
