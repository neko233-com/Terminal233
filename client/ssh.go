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
	"golang.org/x/crypto/ssh/agent"
)

type connectionSettings struct {
	Host, Port, User, Password, KeyPath, Passphrase, Fingerprint string
}

func (s connectionSettings) canUseStoredAuth() bool {
	return s.KeyPath != "" || s.Password != "" || agentConfigured()
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
	return inspectHostThrough(s, nil)
}

func inspectHostThrough(s connectionSettings, via *ssh.Client) (string, error) {
	addr, err := s.address()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialSSHAddress(ctx, addr, via)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
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
	client           *ssh.Client
	done             <-chan struct{}
	session          *ssh.Session
	in               io.WriteCloser
	out              io.Reader
	monitorOnce      sync.Once
	monitorGate      chan struct{}
	integration      shellIntegration
	readPending      []byte
	readPendingError error
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

func (s *remoteShell) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.readPending) > 0 {
		n := min(len(p), len(s.readPending))
		n = s.integration.feedUntilPrompt(s.readPending[:n])
		copy(p, s.readPending[:n])
		clear(s.readPending[:n])
		s.readPending = s.readPending[n:]
		if len(s.readPending) == 0 && s.readPendingError != nil {
			err := s.readPendingError
			s.readPendingError = nil
			return n, err
		}
		return n, nil
	}
	n, err := s.out.Read(p)
	if n > 0 {
		used := s.integration.feedUntilPrompt(p[:n])
		if used < n {
			s.readPending = append(s.readPending[:0], p[used:n]...)
			clear(p[used:n])
			s.readPendingError = err
			return used, nil
		}
	}
	return n, err
}

func (s *remoteShell) NekoPromptState() (uint64, bool) { return s.integration.promptState() }
func (s *remoteShell) Write(p []byte) (int, error)     { return s.in.Write(p) }
func (s *remoteShell) Resize(cols, rows int) error     { return s.session.WindowChange(rows, cols) }
func (s *remoteShell) Close() error {
	if s.in != nil {
		_ = s.in.Close()
	}
	if s.session != nil {
		_ = s.session.Close()
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

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
	return connectShellAtThrough(s, dir, nil)
}

func connectShellAtThrough(s connectionSettings, dir string, via *ssh.Client) (*remoteShell, error) {
	command := ""
	if dir != "" {
		var err error
		command, err = remoteDirectoryCommand(dir)
		if err != nil {
			return nil, err
		}
	}
	client, err := connectSSHClientThrough(context.Background(), s, via)
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
	return connectSSHClientThrough(ctx, s, nil)
}

func dialSSHAddress(ctx context.Context, address string, via *ssh.Client) (net.Conn, error) {
	if via != nil {
		return via.DialContext(ctx, "tcp", address)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

// Each hop uses its own pinned fingerprint and credentials. A tunneled
// connection is still a separate SSH handshake, not an agent-forwarded shell.
func connectSSHClientThrough(ctx context.Context, s connectionSettings, via *ssh.Client) (*ssh.Client, error) {
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
	} else if s.Password != "" {
		auth = ssh.Password(s.Password)
	} else {
		agentCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		agentConn, err := dialLocalSSHAgent(agentCtx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("无法连接本机 SSH agent，请填写密码或私钥：%w", err)
		}
		defer agentConn.Close()
		_ = agentConn.SetDeadline(time.Now().Add(20 * time.Second))
		stopAgent := context.AfterFunc(ctx, func() { _ = agentConn.Close() })
		defer stopAgent()
		auth = ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers)
	}
	cfg := &ssh.ClientConfig{User: s.User, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if subtle.ConstantTimeCompare([]byte(actual), []byte(s.Fingerprint)) != 1 {
			return fmt.Errorf("服务器指纹已变化：%s", actual)
		}
		return nil
	}}
	dialCtx, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	conn, err := dialSSHAddress(dialCtx, addr, via)
	cancelDial()
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	// SSH channel-backed connections need not implement deadlines. Closing
	// on timeout also bounds their handshakes and unblocks cancellation.
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, 15*time.Second)
	defer cancelHandshake()
	stopCancellation := context.AfterFunc(handshakeCtx, func() { _ = conn.Close() })
	cc, channels, requests, err := ssh.NewClientConn(conn, addr, cfg)
	stopCancellation()
	if err != nil {
		_ = conn.Close()
		if handshakeCtx.Err() != nil {
			return nil, handshakeCtx.Err()
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, channels, requests)
	if err := handshakeCtx.Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

const maxSSHJumpHosts = 8

// The returned final client owns the entire route: closing it also closes all
// parent transports. Partial routes are closed on every failure.
func connectSSHRoute(ctx context.Context, hops []connectionSettings) (*ssh.Client, error) {
	if len(hops) == 0 || len(hops) > maxSSHJumpHosts+1 {
		return nil, errors.New("跳板数量无效")
	}
	var parents []*ssh.Client
	var previous *ssh.Client
	for i, settings := range hops {
		client, err := connectSSHClientThrough(ctx, settings, previous)
		if err != nil {
			if previous != nil {
				_ = previous.Close()
			}
			for j := len(parents) - 1; j >= 0; j-- {
				_ = parents[j].Close()
			}
			return nil, fmt.Errorf("第 %d 跳 SSH 连接失败：%w", i+1, err)
		}
		if previous != nil {
			parents = append(parents, previous)
		}
		previous = client
	}
	if len(parents) > 0 {
		final := previous
		go func() {
			_ = final.Wait()
			for i := len(parents) - 1; i >= 0; i-- {
				_ = parents[i].Close()
			}
		}()
	}
	return previous, nil
}
