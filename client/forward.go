package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const maxForwardsPerSession = 8
const maxForwardConnections = 64

type localForward struct {
	listener    net.Listener
	target      string
	mode        string
	client      *ssh.Client
	mu          sync.Mutex
	closed      bool
	connections map[net.Conn]struct{}
	slots       chan struct{}
	stopped     chan struct{}
	lastError   string
	invalidate  func()
}

func parseForwardTarget(host, portText string) (string, error) {
	host = strings.TrimSpace(host)
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if host == "" || strings.ContainsAny(host, "/\\@ \r\n\t\x00") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) || err != nil || port < 1 || port > 65535 {
		return "", errors.New("请输入有效的目标主机和端口")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func startLocalForward(client *ssh.Client, clientDone <-chan struct{}, localPort, host, remotePort string, invalidate func()) (*localForward, error) {
	if client == nil {
		return nil, errors.New("请先连接 SSH 会话")
	}
	port, err := strconv.Atoi(strings.TrimSpace(localPort))
	if err != nil || port < 0 || port > 65535 {
		return nil, errors.New("本机端口应为 0 到 65535")
	}
	target, err := parseForwardTarget(host, remotePort)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("本机端口监听失败：%w", err)
	}
	return runForward(client, clientDone, listener, "local", target, invalidate), nil
}

func startDynamicForward(client *ssh.Client, clientDone <-chan struct{}, portText string, invalidate func()) (*localForward, error) {
	if client == nil {
		return nil, errors.New("请先连接 SSH 会话")
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 0 || port > 65535 {
		return nil, errors.New("本机端口应为 0 到 65535")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("本机端口监听失败：%w", err)
	}
	return runForward(client, clientDone, listener, "dynamic", "SOCKS5", invalidate), nil
}

func startRemoteForward(client *ssh.Client, clientDone <-chan struct{}, portText, host, targetPort string, invalidate func()) (*localForward, error) {
	if client == nil {
		return nil, errors.New("请先连接 SSH 会话")
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 0 || port > 65535 {
		return nil, errors.New("远程端口应为 0 到 65535")
	}
	target, err := parseForwardTarget(host, targetPort)
	if err != nil {
		return nil, err
	}
	listener, err := client.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("远程端口监听失败：%w", err)
	}
	return runForward(client, clientDone, listener, "remote", target, invalidate), nil
}

func runForward(client *ssh.Client, clientDone <-chan struct{}, listener net.Listener, mode, target string, invalidate func()) *localForward {
	f := &localForward{listener: listener, mode: mode, target: target, client: client, connections: make(map[net.Conn]struct{}), slots: make(chan struct{}, maxForwardConnections), stopped: make(chan struct{}), invalidate: invalidate}
	go f.serve()
	go func() {
		select {
		case <-clientDone:
			f.Close()
		case <-f.stopped:
		}
	}()
	return f
}

func (f *localForward) address() string { return f.listener.Addr().String() }

func (f *localForward) add(c net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.connections[c] = struct{}{}
	return true
}
func (f *localForward) remove(c net.Conn) {
	f.mu.Lock()
	delete(f.connections, c)
	f.mu.Unlock()
	_ = c.Close()
}
func (f *localForward) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	close(f.stopped)
	connections := make([]net.Conn, 0, len(f.connections))
	for c := range f.connections {
		connections = append(connections, c)
	}
	f.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
	if f.mode == "remote" {
		// Canceling a remote listener waits for the SSH server; never block UI.
		go f.listener.Close()
	} else {
		_ = f.listener.Close()
	}
	if f.invalidate != nil {
		f.invalidate()
	}
}
func (f *localForward) errorText() string { f.mu.Lock(); defer f.mu.Unlock(); return f.lastError }
func (f *localForward) isClosed() bool    { f.mu.Lock(); defer f.mu.Unlock(); return f.closed }

func (f *localForward) serve() {
	defer f.Close()
	for {
		local, err := f.listener.Accept()
		if err != nil {
			return
		}
		select {
		case f.slots <- struct{}{}:
		default:
			_ = local.Close()
			continue
		}
		if !f.add(local) {
			<-f.slots
			_ = local.Close()
			continue
		}
		go f.relay(local)
	}
}
func (f *localForward) relay(local net.Conn) {
	defer func() { <-f.slots }()
	defer f.remove(local)
	target := f.target
	if f.mode == "dynamic" {
		var err error
		target, err = socks5Target(local)
		if err != nil {
			return
		}
	}
	var remote net.Conn
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		select {
		case <-f.stopped:
			cancel()
		case <-ctx.Done():
		}
	}()
	if f.mode == "remote" {
		remote, err = (&net.Dialer{}).DialContext(ctx, "tcp", target)
	} else {
		remote, err = f.client.DialContext(ctx, "tcp", target)
	}
	if err != nil {
		if f.mode == "dynamic" {
			_, _ = local.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		}
		f.mu.Lock()
		f.lastError = err.Error()
		f.mu.Unlock()
		if f.invalidate != nil {
			f.invalidate()
		}
		return
	}
	if f.mode == "dynamic" {
		if _, err := local.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			_ = remote.Close()
			return
		}
		_ = local.SetDeadline(time.Time{})
	}
	if !f.add(remote) {
		_ = remote.Close()
		return
	}
	defer f.remove(remote)
	done := make(chan struct{}, 2)
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if half, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go copySide(remote, local)
	go copySide(local, remote)
	<-done
	<-done
}

// SOCKS5 is restricted to unauthenticated CONNECT on a loopback listener.
// The hostname is passed through SSH so DNS resolution happens remotely.
func socks5Target(conn net.Conn) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}
	if header[0] != 5 || header[1] == 0 {
		return "", errors.New("SOCKS5 握手无效")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, method := range methods {
		if method == 0 {
			noAuth = true
		}
	}
	if !noAuth {
		_, _ = conn.Write([]byte{5, 255})
		return "", errors.New("SOCKS5 不支持请求的认证方式")
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return "", err
	}
	if req[0] != 5 || req[2] != 0 || req[1] != 1 {
		_, _ = conn.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", errors.New("SOCKS5 仅支持 TCP CONNECT")
	}
	var length int
	switch req[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		if _, err := io.ReadFull(conn, header[:1]); err != nil {
			return "", err
		}
		length = int(header[0])
	default:
		_, _ = conn.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", errors.New("SOCKS5 地址类型无效")
	}
	if length == 0 {
		return "", errors.New("SOCKS5 目标为空")
	}
	addr := make([]byte, length+2)
	if _, err := io.ReadFull(conn, addr); err != nil {
		return "", err
	}
	host := string(addr[:length])
	if req[3] != 3 {
		host = net.IP(addr[:length]).String()
	}
	port := int(addr[length])<<8 | int(addr[length+1])
	target, err := parseForwardTarget(host, strconv.Itoa(port))
	if err != nil {
		_, _ = conn.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", err
	}
	return target, nil
}
