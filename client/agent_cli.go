package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const agentOutputLimit = 2 << 20
const agentTransferLimit = 64 << 20

type agentHost struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Group       string `json:"group,omitempty"`
	Host        string `json:"host"`
	Port        string `json:"port"`
	User        string `json:"user"`
	Trusted     bool   `json:"trusted"`
	Credentials bool   `json:"credentials_saved"`
}

func agentHostID(p profile) string {
	if p.StableID != "" {
		return "local:" + p.StableID
	}
	// Old files have row indexes, which change when hosts are reordered. Bind a
	// legacy selector to its endpoint instead; ambiguous duplicates are refused.
	s := p.Settings
	s.Password, s.Passphrase = "", ""
	b, _ := json.Marshal(s)
	digest := sha256.Sum256(b)
	return "legacy:" + hex.EncodeToString(digest[:])
}

func (d *desktop) agentProfile(id string) (profile, error) {
	var found profile
	count := 0
	for _, p := range d.profiles {
		if agentHostID(p) == id {
			found, count = p, count+1
		}
	}
	if count != 1 {
		return profile{}, errors.New("主机 ID 不存在或不唯一；请重新运行 agent hosts，并在界面中保存重复的旧主机")
	}
	return found, nil
}

// This is a one-shot local client, not a listener. DPAPI and host-key checks
// are shared with the GUI; no password option or credential-export operation
// exists. stdout is exactly one JSON document, including failures.
func runAgentCLI(args []string, store string, stdout io.Writer) int {
	operation := "help"
	if len(args) > 0 {
		operation = args[0]
	}
	write := func(v any, code int) int {
		if json.NewEncoder(stdout).Encode(v) != nil {
			return 2
		}
		return code
	}
	fail := func(message string, code int) int {
		return write(map[string]any{"ok": false, "operation": operation, "error": message}, code)
	}
	if operation == "help" || operation == "--help" || operation == "-h" {
		return write(map[string]any{"ok": true, "operation": "help", "commands": []string{
			"agent hosts", "agent exec --host ID --command COMMAND [--timeout 30s]",
			"agent ls --host ID --path /directory", "agent get --host ID --path /remote/file --local C:/file",
			"agent put --host ID --local C:/file --path /remote/file",
		}, "authentication": "使用当前 Windows 用户保存的凭据及已核对指纹；无需密码参数", "output": "JSON", "version": version}, 0)
	}
	if operation != "hosts" && operation != "exec" && operation != "ls" && operation != "get" && operation != "put" {
		return fail("未知操作；请运行 agent help", 2)
	}
	f := flag.NewFlagSet("agent "+operation, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("host", "", "主机 ID")
	command := f.String("command", "", "远程命令")
	remote := f.String("path", ".", "远程路径")
	local := f.String("local", "", "本机文件路径")
	timeout := f.Duration("timeout", 30*time.Second, "请求时限")
	if err := f.Parse(args[1:]); err != nil || len(f.Args()) != 0 {
		return fail("参数无效；请运行 agent help（不接受密码参数）", 2)
	}
	if *timeout <= 0 || *timeout > 10*time.Minute || len(*command) > 65536 || strings.IndexByte(*command, 0) >= 0 {
		return fail("时限应在 0 到 10 分钟之间，命令最多 64 KiB 且不能含 NUL", 2)
	}
	if (operation == "ls" && *local != "") || ((operation == "get" || operation == "put") && (*local == "" || *remote == "" || *remote == ".")) {
		return fail("文件参数无效：ls 不接受 local；get/put 必须提供 path 和 local", 2)
	}
	d := &desktop{}
	if err := d.loadProfiles(store); err != nil {
		return fail("读取本机主机失败："+err.Error(), 2)
	}
	if operation == "hosts" {
		if *id != "" || *command != "" || *local != "" || *remote != "." {
			return fail("hosts 不接受连接或文件参数", 2)
		}
		hosts := make([]agentHost, 0, len(d.profiles))
		for _, p := range d.profiles {
			s := p.Settings
			hosts = append(hosts, agentHost{agentHostID(p), p.Label, p.Group, s.Host, s.Port, s.User, strings.HasPrefix(s.Fingerprint, "SHA256:"), p.EncryptedCredentials != "" || p.IdentityID != "" || s.KeyPath != ""})
		}
		return write(map[string]any{"ok": true, "operation": operation, "hosts": hosts}, 0)
	}
	if *id == "" || (operation == "exec" && strings.TrimSpace(*command) == "") || (operation != "exec" && *command != "") || (operation == "exec" && (*local != "" || *remote != ".")) {
		return fail("缺少主机或命令，或参数不适用于当前操作；请运行 agent help", 2)
	}
	p, err := d.agentProfile(*id)
	if err != nil {
		return fail(err.Error(), 2)
	}
	if !strings.HasPrefix(p.Settings.Fingerprint, "SHA256:") {
		return fail("请先在 NekoSSH 界面核对该主机指纹", 2)
	}
	settings, err := d.profileCredential(p)
	if err != nil {
		return fail(err.Error(), 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client, err := connectSSHClient(ctx, settings)
	settings.Password, settings.Passphrase = "", ""
	if err != nil {
		return fail("SSH 连接失败："+err.Error(), 2)
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	if operation == "exec" {
		session, err := client.NewSession()
		if err != nil {
			return fail("无法打开命令通道："+err.Error(), 2)
		}
		defer session.Close()
		out, stderr := &agentCapture{}, &agentCapture{}
		session.Stdout, session.Stderr = out, stderr
		err = session.Run(*command)
		code := 0
		if ctx.Err() != nil {
			return fail("远程命令超时；连接已关闭，远端程序是否结束需自行核对", 124)
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitStatus()
			if code < 1 || code > 255 {
				code = 255
			}
		} else if err != nil {
			return fail("远程命令未能完成："+err.Error(), 2)
		}
		return write(map[string]any{"ok": code == 0, "operation": operation, "exit_code": code, "stdout": string(out.data), "stderr": string(stderr.data), "stdout_truncated": out.truncated, "stderr_truncated": stderr.truncated}, min(max(code, 0), 255))
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fail("无法打开文件通道："+err.Error(), 2)
	}
	defer sftpClient.Close()
	if operation == "ls" {
		if *local != "" {
			return fail("ls 不接受 local 参数", 2)
		}
		entries, err := sftpClient.ReadDirContext(ctx, *remote)
		if err != nil {
			return fail("读取目录失败："+err.Error(), 2)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		type entry struct {
			Name      string `json:"name"`
			Size      int64  `json:"size"`
			Mode      string `json:"mode"`
			Directory bool   `json:"directory"`
		}
		items := make([]entry, 0, min(len(entries), 5000))
		for _, info := range entries[:min(len(entries), 5000)] {
			items = append(items, entry{info.Name(), info.Size(), info.Mode().String(), info.IsDir()})
		}
		return write(map[string]any{"ok": true, "operation": operation, "entries": items, "truncated": len(entries) > len(items)}, 0)
	}
	if *local == "" || *remote == "" || *remote == "." {
		return fail("文件传输需要 path 和 local；目标文件必须尚不存在", 2)
	}
	if err := agentTransfer(sftpClient, operation, *remote, *local); err != nil {
		return fail("文件传输失败："+err.Error(), 2)
	}
	return write(map[string]any{"ok": true, "operation": operation}, 0)
}

type agentCapture struct {
	data      []byte
	truncated bool
}

func (w *agentCapture) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, agentOutputLimit-len(w.data))
	w.data = append(w.data, p[:keep]...)
	w.truncated = w.truncated || keep < n
	return n, nil
}

func agentTransfer(client *sftp.Client, operation, remote, local string) error {
	var source io.ReadCloser
	var target io.WriteCloser
	var remove func() error
	var size int64
	var unchanged func() bool
	if operation == "get" {
		info, err := client.Lstat(remote)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > agentTransferLimit {
			return errors.New("仅支持不超过 64 MiB 的普通文件")
		}
		file, err := client.Open(remote)
		if err != nil {
			return err
		}
		source = file
		defer source.Close()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() || opened.Size() != info.Size() || !opened.ModTime().Equal(info.ModTime()) {
			return errors.New("远程文件在读取前已变化")
		}
		size = opened.Size()
		unchanged = func() bool {
			next, err := file.Stat()
			return err == nil && next.Size() == size && next.ModTime().Equal(opened.ModTime()) && next.Mode() == opened.Mode()
		}
		localFile, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		target, remove = localFile, func() error { return os.Remove(local) }
	} else {
		info, err := os.Lstat(local)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > agentTransferLimit {
			return errors.New("仅支持不超过 64 MiB 的普通文件")
		}
		file, err := os.Open(local)
		if err != nil {
			return err
		}
		source = file
		defer source.Close()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() != info.Size() || !opened.ModTime().Equal(info.ModTime()) {
			return errors.New("本机文件在读取前已变化")
		}
		size = opened.Size()
		unchanged = func() bool {
			next, err := file.Stat()
			return err == nil && next.Size() == size && next.ModTime().Equal(opened.ModTime()) && next.Mode() == opened.Mode()
		}
		remoteFile, err := client.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return err
		}
		target, remove = remoteFile, func() error { return client.Remove(remote) }
		if err = remoteFile.Chmod(0600); err != nil {
			_ = target.Close()
			_ = remove()
			return err
		}
	}
	n, err := io.Copy(target, io.LimitReader(source, agentTransferLimit+1))
	if n > agentTransferLimit {
		err = errors.New("文件在传输时超过 64 MiB")
	}
	if err == nil && (n != size || !unchanged()) {
		err = errors.New("源文件在传输期间已变化")
	}
	closeErr := target.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		if cleanupErr := remove(); cleanupErr != nil {
			return fmt.Errorf("%w；部分目标文件清理失败：%v", err, cleanupErr)
		}
		return err
	}
	return nil
}

func agentProfileStore() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "NekoSSH", "profiles.json"), nil
}
