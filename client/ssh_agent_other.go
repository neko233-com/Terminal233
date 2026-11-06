//go:build !windows

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
)

func agentConfigured() bool { return os.Getenv("SSH_AUTH_SOCK") != "" }

func dialLocalSSHAgent(ctx context.Context) (net.Conn, error) {
	address := os.Getenv("SSH_AUTH_SOCK")
	if !filepath.IsAbs(address) {
		return nil, errors.New("SSH_AUTH_SOCK 必须是本机绝对路径")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", address)
}
