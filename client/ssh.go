package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type connectionSettings struct {
	Host, Port, User, Password, KeyPath, Passphrase, Fingerprint string
}

func (s connectionSettings) address() (string, error) {
	host := strings.TrimSpace(s.Host)
	port, err := strconv.Atoi(strings.TrimSpace(s.Port))
	if host == "" || strings.ContainsAny(host, "/\\@ \r\n\t") || err != nil || port < 1 || port > 65535 || strings.TrimSpace(s.User) == "" {
		return "", errors.New("请输入有效的主机、端口和用户名")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// inspectHost stops the SSH handshake before credentials can be sent.
func inspectHost(s connectionSettings) (string, error) {
	addr, err := s.address()
	if err != nil {
		return "", err
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var fingerprint string
	cfg := &ssh.ClientConfig{User: s.User, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		return errors.New("主机指纹待确认")
	}}
	c, _, _, err := ssh.NewClientConn(conn, addr, cfg)
	if c != nil {
		_ = c.Close()
	}
	if fingerprint != "" {
		return fingerprint, nil
	}
	return "", err
}

type remoteShell struct {
	client      *ssh.Client
	done        <-chan struct{}
	session     *ssh.Session
	in          io.WriteCloser
	out         io.Reader
	monitorOnce sync.Once
	monitorGate chan struct{}
}

func (s *remoteShell) takeMonitorSlot() bool {
	s.monitorOnce.Do(func() { s.monitorGate = make(chan struct{}, 1) })
	select {
	case s.monitorGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *remoteShell) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *remoteShell) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *remoteShell) Resize(cols, rows int) error { return s.session.WindowChange(rows, cols) }
func (s *remoteShell) Close() error                { _ = s.in.Close(); _ = s.session.Close(); return s.client.Close() }

func connectShell(s connectionSettings) (*remoteShell, error) {
	return connectShellAt(s, "")
}

// quotePOSIX passes untrusted paths as literal data, including quotes and
// shell metacharacters. It must be used for each nesting level of sh -c.
func quotePOSIX(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func remoteDirectoryCommand(dir string) (string, error) {
	cleaned, err := terminalDirectory(dir, false)
	if err != nil || cleaned != dir || !validWorkspaceText(dir, 4096) {
		return "", errors.New("远程终端目录无效")
	}
	script := "if ! cd " + quotePOSIX(dir) + "; then printf '%s\\n' 'NekoSSH：无法恢复终端目录，已进入默认目录。' >&2; fi; exec \"${SHELL:-/bin/sh}\" -i"
	return "/bin/sh -c " + quotePOSIX(script), nil
}

func connectShellAt(s connectionSettings, dir string) (*remoteShell, error) {
	command := ""
	if dir != "" {
		var err error
		command, err = remoteDirectoryCommand(dir)
		if err != nil {
			return nil, err
		}
	}
	client, err := connectSSHClient(context.Background(), s)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*remoteShell, error) { _ = client.Close(); return nil, err }
	session, err := client.NewSession()
	if err != nil {
		return fail(err)
	}
	if err = session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
		return fail(err)
	}
	in, err := session.StdinPipe()
	if err != nil {
		return fail(err)
	}
	out, output := io.Pipe()
	session.Stdout = output
	session.Stderr = output
	if command == "" {
		err = session.Shell()
	} else {
		err = session.Start(command)
	}
	if err != nil {
		_ = out.Close()
		_ = output.Close()
		return fail(err)
	}
	go func() {
		_ = output.CloseWithError(session.Wait())
		_ = client.Close()
	}()
	done := make(chan struct{})
	go func() { _ = client.Wait(); close(done) }()
	return &remoteShell{client: client, done: done, session: session, in: in, out: out}, nil
}

// A forwarding rule needs only an SSH transport, not a shell or PTY. Both
// paths use the same host-key verification and authentication policy.
func connectSSHClient(ctx context.Context, s connectionSettings) (*ssh.Client, error) {
	addr, err := s.address()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(s.Fingerprint, "SHA256:") {
		return nil, errors.New("请先确认服务器的 SHA256 指纹")
	}
	var auth ssh.AuthMethod
	if s.KeyPath != "" {
		pem, err := os.ReadFile(s.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥失败: %w", err)
		}
		var signer ssh.Signer
		if s.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(s.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	} else {
		if s.Password == "" {
			return nil, errors.New("请输入密码或私钥路径")
		}
		auth = ssh.Password(s.Password)
	}
	cfg := &ssh.ClientConfig{User: s.User, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if subtle.ConstantTimeCompare([]byte(actual), []byte(s.Fingerprint)) != 1 {
			return fmt.Errorf("服务器指纹已变化：%s", actual)
		}
		return nil
	}}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	stopCancellation := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cc, channels, requests, err := ssh.NewClientConn(conn, addr, cfg)
	stopCancellation()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, channels, requests)
	if err := ctx.Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
