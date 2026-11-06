package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const maxEditJobs = 12

type externalEditJob struct {
	id                                 int
	shell                              *remoteShell
	dir, name, host, localPath, status string
	baseline                           editSnapshot
	cancel                             context.CancelFunc
	generation                         int
	stopping                           bool
}

func defaultEditorPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("WINDIR"), "System32", "notepad.exe")
	}
	return ""
}
func validEditorPath(p string) bool {
	return filepath.IsAbs(p) && len(p) <= 32767 && !strings.ContainsAny(p, "\x00\r\n") && (runtime.GOOS != "windows" || strings.EqualFold(filepath.Ext(p), ".exe"))
}

var startExternalEditor = func(editor, file string) error {
	if !validEditorPath(editor) {
		return errors.New("请选择有效的编辑器程序")
	}
	command := exec.Command(editor, file)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func readLocalEdit(filename string) ([]byte, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	before, err := os.Lstat(filename)
	if err != nil {
		return nil, zero, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxExternalEditSize {
		return nil, zero, errors.New("本机副本不是不超过 16 MiB 的普通文件")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, zero, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, zero, errors.New("本机副本正在变化")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxExternalEditSize+1))
	if err != nil {
		return nil, zero, err
	}
	after, err := os.Lstat(filename)
	if err != nil || !os.SameFile(opened, after) || !opened.ModTime().Equal(after.ModTime()) || after.Size() != int64(len(data)) || len(data) > maxExternalEditSize {
		return nil, zero, errors.New("本机副本正在变化")
	}
	return data, sha256.Sum256(data), nil
}

func (d *desktop) selectedEditor() string {
	if d.externalEditorPath != "" {
		return d.externalEditorPath
	}
	return defaultEditorPath()
}

// A copy is kept under the user's temporary directory even after monitoring
// stops or the app exits, so an interrupted edit can be recovered.
func createEditCopy(name string, contents []byte) (string, error) {
	if !validDirectoryName(name) {
		return "", errors.New("文件名称无效")
	}
	dir, err := os.MkdirTemp("", "nekossh-edit-")
	if err != nil {
		return "", err
	}
	if err := protectEditDirectory(dir); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	filename := filepath.Join(dir, name)
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(filename)
		_ = os.Remove(dir)
		if writeErr != nil {
			return "", writeErr
		}
		return "", closeErr
	}
	return filename, nil
}

func (d *desktop) openLocalExternalEdit(dir, name string) {
	if d.busy || !validDirectoryName(name) {
		return
	}
	file, err := localItemPath(dir, name)
	if err == nil {
		var info os.FileInfo
		info, err = os.Lstat(file)
		if err == nil && !info.Mode().IsRegular() {
			err = errors.New("只能编辑普通文件")
		}
	}
	if err == nil {
		err = startExternalEditor(d.selectedEditor(), file)
	}
	if err != nil {
		d.fileStatus = "打开编辑器失败：" + err.Error()
	}
}

func (d *desktop) openExternalEdit(shell *remoteShell, dir, name string) {
	if d.busy || d.win == nil || shell == nil || !validDirectoryName(name) {
		return
	}
	if len(d.externalEdits) >= maxEditJobs {
		d.fileStatus = "编辑任务已达上限，请先关闭已停止的任务"
		return
	}
	editor := d.selectedEditor()
	if !validEditorPath(editor) {
		d.fileStatus = "请选择外部编辑器"
		return
	}
	origin := "远程主机"
	for _, session := range d.sessions {
		if session.shell == shell {
			origin = session.label
			if session.active.Host != "" {
				origin = session.active.User + "@" + session.active.Host + ":" + session.active.Port
			}
			break
		}
	}
	d.busy = true
	d.fileStatus = "正在准备远程文件…"
	win := d.win
	go func() {
		contents, base, err := loadExternalEdit(shell, dir, name)
		var local string
		if err == nil {
			local, err = createEditCopy(name, contents)
		}
		if err == nil {
			err = startExternalEditor(editor, local)
		}
		win.Update(func() {
			d.busy = false
			if err != nil {
				d.fileStatus = "打开编辑器失败：" + err.Error()
				if local != "" {
					d.fileStatus += "；本机副本：" + local
				}
				return
			}
			d.nextExternalEditID++
			job := &externalEditJob{id: d.nextExternalEditID, shell: shell, dir: dir, name: name, host: origin, localPath: local, baseline: base, status: "自动上传已开启"}
			d.externalEdits = append(d.externalEdits, job)
			d.showExternalEdits = true
			d.fileStatus = "已在编辑器中打开：" + name
			d.watchExternalEdit(job)
		})
	}()
}

func (d *desktop) watchExternalEdit(job *externalEditJob) {
	if d.win == nil || job.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel
	job.stopping = false
	job.generation++
	generation := job.generation
	baseline := job.baseline
	shell, dir, name, localPath := job.shell, job.dir, job.name, job.localPath
	win := d.win
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var candidate [sha256.Size]byte
		stable := 0
		lastReadError := ""
		var lastInfo os.FileInfo
		var lastRead time.Time
		finish := func(status string, saved editSnapshot) {
			win.Update(func() {
				if job.generation != generation {
					return
				}
				job.baseline = saved
				job.cancel = nil
				job.stopping = false
				job.status = status
			})
		}
		for {
			select {
			case <-ctx.Done():
				finish("监控已停止；本机副本保留", baseline)
				return
			case <-ticker.C:
				info, statErr := os.Lstat(localPath)
				if statErr == nil && stable == 0 && lastInfo != nil && os.SameFile(lastInfo, info) && lastInfo.Size() == info.Size() && lastInfo.ModTime().Equal(info.ModTime()) && lastInfo.Mode() == info.Mode() && time.Since(lastRead) < 15*time.Second {
					continue
				}
				data, digest, err := readLocalEdit(localPath)
				if err != nil {
					stable = 0
					message := err.Error()
					if message != lastReadError {
						lastReadError = message
						win.Update(func() {
							if job.generation == generation && !job.stopping {
								job.status = "等待本机副本：" + message
							}
						})
					}
					continue
				}
				lastInfo, lastRead = info, time.Now()
				if lastReadError != "" {
					lastReadError = ""
					win.Update(func() {
						if job.generation == generation && !job.stopping {
							job.status = "自动上传已开启"
						}
					})
				}
				if digest == baseline.digest {
					stable = 0
					continue
				}
				if digest != candidate {
					candidate = digest
					stable = 1
					continue
				}
				stable++
				if stable < 2 {
					continue
				}
				if ctx.Err() != nil {
					finish("监控已停止；本机副本保留", baseline)
					return
				}
				win.Update(func() {
					if job.generation == generation && !job.stopping {
						job.status = "正在上传更改…"
					}
				})
				next, err := saveExternalEdit(shell, dir, name, baseline, data)
				if err != nil {
					finish("上传停止："+err.Error()+"；本机副本保留", baseline)
					return
				}
				baseline = next
				stable = 0
				win.Update(func() {
					if job.generation == generation {
						d.applyExternalEditInfo(shell, dir, name, next)
						job.baseline = next
						if !job.stopping {
							job.status = "已上传，继续监控"
						}
					}
				})
			}
		}
	}()
}

func (d *desktop) applyExternalEditInfo(shell *remoteShell, dir, name string, snapshot editSnapshot) {
	update := func(files []remoteFile) {
		for i := range files {
			if files[i].Name == name && eligibleBatchFile(files[i]) {
				files[i].Size, files[i].Modified = snapshot.size, snapshot.modified
			}
		}
	}
	if d.shell == shell && d.fileDir == dir {
		update(d.files)
	}
	if d.secondaryPane.shell == shell && d.secondaryPane.directory == dir {
		update(d.secondaryPane.files)
	}
	for _, session := range d.sessions {
		if session.shell == shell && session.fileDir == dir {
			update(session.files)
		}
	}
}

func (d *desktop) externalEditJobsView(c *ui.Context) {
	if len(d.externalEdits) == 0 {
		return
	}
	button := ui.Button(c.Key("external-edits-toggle"), fmt.Sprintf("编辑任务 %d", len(d.externalEdits)))
	if button.Clicked() {
		d.showExternalEdits = !d.showExternalEdits
	}
	ui.PopoverBase(c, button, &d.showExternalEdits, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(4, 0, 0, 0).Children(func() {
			width, _ := c.Size()
			ui.Column(c).Width(min(float32(440), width-40)).Padding(12).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() { d.externalEditJobsContent(c) })
		})
	})
}

func (d *desktop) externalEditJobsContent(c *ui.Context) {
	ui.Text(c, "自动替换可能改变 ACL 或扩展属性；并发修改仍可能发生。 ").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
	ui.Scroll(c.Key("external-edits-list")).Label("外部编辑任务").Height(155).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			for _, entry := range d.externalEdits {
				job := entry
				ui.Column(c.Key(fmt.Sprintf("external-edit-%d", job.id))).FillWidth().Gap(4).Children(func() {
					ui.Text(c, job.host+" · "+job.dir+"/"+job.name).FontSize(12).SingleLine().Tooltip(job.host + " · " + job.dir + "/" + job.name)
					ui.Text(c, job.status).FontSize(12).Wrap().Tooltip("自动保存会原子替换文件；SFTP 无法保留 ACL、扩展属性，也无法完全避免并发修改。")
					ui.TextInput(c.Key(fmt.Sprintf("external-edit-path-%d", job.id)), &job.localPath).Label("本机编辑副本：" + job.name).ReadOnly(true).FillWidth()
					ui.Row(c).Gap(6).Children(func() {
						if job.cancel != nil {
							if ui.Button(c.Key(fmt.Sprintf("external-edit-stop-%d", job.id)), "停止监控").Disabled(job.stopping).Clicked() {
								job.stopping = true
								job.status = "等待当前上传结束…"
								job.cancel()
							}
						} else {
							if ui.Button(c.Key(fmt.Sprintf("external-edit-retry-%d", job.id)), "重新监控").Clicked() {
								job.status = "自动上传已开启"
								d.watchExternalEdit(job)
							}
							if ui.Button(c.Key(fmt.Sprintf("external-edit-close-%d", job.id)), "关闭任务（保留副本）").Clicked() {
								d.removeExternalEdit(job)
							}
						}
						if ui.Button(c.Key(fmt.Sprintf("external-edit-copy-%d", job.id)), "复制本机路径").Clicked() {
							mygo.Clipboard.WriteText(job.localPath)
						}
					})
				})
			}
		})
	})
}

func (d *desktop) removeExternalEdit(job *externalEditJob) {
	if job.cancel != nil {
		return
	}
	for i, item := range d.externalEdits {
		if item == job {
			d.externalEdits = append(d.externalEdits[:i], d.externalEdits[i+1:]...)
			return
		}
	}
}
func (d *desktop) stopExternalEdits() {
	for _, job := range d.externalEdits {
		if job.cancel != nil {
			job.cancel()
		}
	}
}

func (d *desktop) pickExternalEditor() {
	if d.busy || d.win == nil {
		return
	}
	d.busy = true
	win := d.win
	defaultPath := d.selectedEditor()
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "选择外部文本编辑器", DefaultPath: defaultPath})
		win.Update(func() {
			d.busy = false
			if err != nil {
				d.preferencesStatus = "选择编辑器失败：" + err.Error()
				return
			}
			if len(paths) == 0 {
				return
			}
			if !validEditorPath(paths[0]) {
				d.preferencesStatus = "请选择有效的编辑器程序"
				return
			}
			d.externalEditorPath = paths[0]
			if d.preferencesPath != "" {
				if err := updatePreferences(d.preferencesPath, func(p *workspacePreferences) error { p.ExternalEditor = paths[0]; return nil }); err != nil {
					d.preferencesStatus = "编辑器路径未保存：" + err.Error()
					return
				}
			}
			d.preferencesStatus = "编辑器已选择"
		})
	}()
}
