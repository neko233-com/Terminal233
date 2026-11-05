package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

const maxForwardsPerSession = 8
const maxForwardConnections = 64

type localForward struct {
	listener    net.Listener
	target      string
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
		return "", errors.New("请输入有效的远程目标主机和端口")
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
	f := &localForward{listener: listener, target: target, client: client, connections: make(map[net.Conn]struct{}), slots: make(chan struct{}, maxForwardConnections), stopped: make(chan struct{}), invalidate: invalidate}
	go f.serve()
	go func() {
		select {
		case <-clientDone:
			f.Close()
		case <-f.stopped:
		}
	}()
	return f, nil
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
	_ = f.listener.Close()
	for _, c := range connections {
		_ = c.Close()
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
	remote, err := f.client.Dial("tcp", f.target)
	if err != nil {
		f.mu.Lock()
		f.lastError = err.Error()
		f.mu.Unlock()
		if f.invalidate != nil {
			f.invalidate()
		}
		return
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
