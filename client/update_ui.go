package main

import (
	"context"
	"errors"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (d *desktop) startUpdateCheck() {
	if d.win == nil || d.updateChecking || d.updateDownloading {
		return
	}
	d.updateChecking = true
	d.updateAvailable = nil
	d.updateStatus = "正在检查项目发布…"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	d.updateCancel = cancel
	win := d.win
	go func() {
		defer cancel()
		candidate, err := checkProjectUpdate(ctx, updateHTTPClient(), version, runtime.GOOS, runtime.GOARCH)
		win.Update(func() {
			d.updateChecking, d.updateCancel = false, nil
			if err != nil {
				d.updateStatus = "检查更新失败：" + err.Error()
				return
			}
			d.updateAvailable = candidate
			if candidate == nil {
				d.updateStatus = "当前已是最新发布版本"
			} else {
				d.updateStatus = "发现新版 " + candidate.Version + "；Windows 安装包未签名。"
			}
		})
	}()
}

func (d *desktop) chooseUpdateDownload() {
	if d.win == nil || d.updateAvailable == nil || d.updateChecking || d.updateDownloading {
		return
	}
	candidate, win := *d.updateAvailable, d.win
	d.updateDownloading, d.updatePath = true, ""
	d.updateStatus = "选择安装包保存位置…"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	d.updateCancel = cancel
	go func() {
		defer cancel()
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: "保存更新安装包（不覆盖已有文件）", DefaultPath: candidate.Name})
		if err == nil && path != "" {
			err = downloadProjectUpdate(ctx, updateHTTPClient(), candidate, path)
		}
		win.Update(func() {
			d.updateDownloading, d.updateCancel = false, nil
			switch {
			case errors.Is(err, context.Canceled):
				d.updateStatus = "已取消更新下载"
			case err != nil:
				d.updateStatus = "更新下载失败：" + transferErrorText(err)
			case path == "":
				d.updateStatus = "已取消选择保存位置"
			default:
				d.updatePath = path
				d.updateStatus = "安装包 SHA-256 已验证；文件未签名，尚未运行。"
			}
		})
	}()
}

func (d *desktop) updateView(c *ui.Context) {
	ui.Text(c, "版本 "+version).FontSize(12).TextColor(c.Theme().TextMuted)
	if ui.Button(c.Key("update-check"), "检查更新").Disabled(d.updateChecking || d.updateDownloading).Clicked() {
		d.startUpdateCheck()
	}
	if d.updateStatus != "" {
		ui.Text(c, d.updateStatus).FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
	}
	if d.updateAvailable != nil {
		if ui.Button(c.Key("update-download"), "下载安装包…").Disabled(d.updateChecking || d.updateDownloading).Clicked() {
			d.chooseUpdateDownload()
		}
	}
	if d.updateDownloading {
		if ui.Button(c.Key("update-cancel"), "取消更新下载").Clicked() && d.updateCancel != nil {
			d.updateCancel()
		}
	}
	if d.updatePath != "" {
		if ui.Button(c.Key("update-show-file"), "显示已下载安装包").Clicked() {
			path := d.updatePath
			go mygo.Shell.ShowItemInFolder(path)
		}
	}
}
