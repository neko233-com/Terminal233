package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/pkg/sftp"
)

const maxExternalEditSize = 16 << 20

var errEditConflict = errors.New("远程文件已变化，请比较本机副本后再处理")

type editSnapshot struct {
	digest   [sha256.Size]byte
	mode     os.FileMode
	uid, gid uint32
	size     int64
	modified int64
}

func editMetadata(info os.FileInfo) (editSnapshot, error) {
	if !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() < 0 || info.Size() > maxExternalEditSize {
		return editSnapshot{}, errors.New("只能编辑不超过 16 MiB 的普通文件")
	}
	stat, ok := info.Sys().(*sftp.FileStat)
	if !ok {
		return editSnapshot{}, errors.New("无法核对远程文件权限和属主")
	}
	return editSnapshot{mode: info.Mode(), uid: stat.UID, gid: stat.GID, size: info.Size(), modified: info.ModTime().Unix()}, nil
}

func readEditSnapshot(client *sftp.Client, remote string) ([]byte, editSnapshot, error) {
	before, err := client.Lstat(remote)
	if err != nil {
		return nil, editSnapshot{}, err
	}
	meta, err := editMetadata(before)
	if err != nil {
		return nil, editSnapshot{}, err
	}
	file, err := client.Open(remote)
	if err != nil {
		return nil, editSnapshot{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, editSnapshot{}, err
	}
	openedMeta, err := editMetadata(opened)
	if err != nil || openedMeta != meta {
		return nil, editSnapshot{}, errEditConflict
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxExternalEditSize+1))
	if err != nil {
		return nil, editSnapshot{}, err
	}
	if len(contents) > maxExternalEditSize || int64(len(contents)) != meta.size {
		return nil, editSnapshot{}, errEditConflict
	}
	after, err := client.Lstat(remote)
	if err != nil {
		return nil, editSnapshot{}, err
	}
	afterMeta, err := editMetadata(after)
	if err != nil || afterMeta != meta {
		return nil, editSnapshot{}, errEditConflict
	}
	meta.digest = sha256.Sum256(contents)
	return contents, meta, nil
}

func loadExternalEdit(shell *remoteShell, dir, name string) ([]byte, editSnapshot, error) {
	if shell == nil || dir == "" || !validDirectoryName(name) {
		return nil, editSnapshot{}, errors.New("请选择远程普通文件")
	}
	var contents []byte
	var snapshot editSnapshot
	err := withSFTP(shell, func(client *sftp.Client) error {
		var e error
		contents, snapshot, e = readEditSnapshot(client, path.Join(dir, name))
		return e
	})
	return contents, snapshot, err
}

// SFTP v3 has no compare-and-swap. Verify the bytes and attributes immediately
// before an atomic replacement; a concurrent writer can still race that check.
func saveExternalEdit(shell *remoteShell, dir, name string, expected editSnapshot, contents []byte) (editSnapshot, error) {
	if shell == nil || dir == "" || !validDirectoryName(name) || len(contents) > maxExternalEditSize {
		return expected, errors.New("编辑内容或目标无效")
	}
	var saved editSnapshot
	err := withSFTP(shell, func(client *sftp.Client) (resultErr error) {
		if _, ok := client.HasExtension("posix-rename@openssh.com"); !ok {
			return errors.New("服务器不支持原子替换，未上传；本机副本仍保留")
		}
		target := path.Join(dir, name)
		_, current, e := readEditSnapshot(client, target)
		if e != nil {
			return e
		}
		if current != expected {
			return errEditConflict
		}
		token := make([]byte, 16)
		if _, e = rand.Read(token); e != nil {
			return e
		}
		stage := path.Join(dir, ".nekossh-edit-"+hex.EncodeToString(token))
		dst, e := client.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if e != nil {
			return e
		}
		created := true
		defer func() {
			if created {
				if cleanupErr := client.Remove(stage); cleanupErr != nil {
					resultErr = fmt.Errorf("%v；无法清理远程临时文件 %s：%w", resultErr, stage, cleanupErr)
				}
			}
		}()
		if e = dst.Chmod(0600); e != nil {
			_ = dst.Close()
			return e
		}
		_, e = io.Copy(dst, bytes.NewReader(contents))
		if e != nil {
			_ = dst.Close()
			return e
		}
		if e = dst.Close(); e != nil {
			return e
		}
		tempInfo, e := client.Lstat(stage)
		if e != nil {
			return e
		}
		tempMeta, e := editMetadata(tempInfo)
		if e != nil {
			return e
		}
		if tempMeta.uid != expected.uid || tempMeta.gid != expected.gid {
			if e = client.Chown(stage, int(expected.uid), int(expected.gid)); e != nil {
				return fmt.Errorf("无法保留原文件属主或用户组：%w", e)
			}
		}
		if e = client.Chmod(stage, expected.mode.Perm()); e != nil {
			return e
		}
		tempInfo, e = client.Lstat(stage)
		if e != nil {
			return e
		}
		tempMeta, e = editMetadata(tempInfo)
		if e != nil {
			return e
		}
		if tempMeta.uid != expected.uid || tempMeta.gid != expected.gid || tempMeta.mode.Perm() != expected.mode.Perm() {
			return errors.New("无法保留原文件属主、用户组或权限")
		}
		_, current, e = readEditSnapshot(client, target)
		if e != nil {
			return e
		}
		if current != expected {
			return errEditConflict
		}
		if e = client.PosixRename(stage, target); e != nil {
			return e
		}
		created = false
		_, saved, e = readEditSnapshot(client, target)
		if e != nil {
			return fmt.Errorf("上传结果未能确认，请核对服务器文件：%w", e)
		}
		if saved.digest != sha256.Sum256(contents) {
			return errors.New("上传后文件内容已改变，请核对服务器文件")
		}
		return nil
	})
	if err != nil {
		return expected, err
	}
	return saved, nil
}
