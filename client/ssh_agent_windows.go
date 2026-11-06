//go:build windows

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
)

func agentConfigured() bool { return true }

func dialLocalSSHAgent(ctx context.Context) (net.Conn, error) {
	address := os.Getenv("SSH_AUTH_SOCK")
	if address == "" {
		address = `\\.\pipe\openssh-ssh-agent`
	}
	if strings.HasPrefix(strings.ToLower(address), `\\.\pipe\`) {
		return winio.DialPipeContext(ctx, address)
	}
	if !filepath.IsAbs(address) || strings.HasPrefix(address, `\\`) {
		return nil, errors.New("SSH_AUTH_SOCK 必须是本机套接字或命名管道")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", address)
}
