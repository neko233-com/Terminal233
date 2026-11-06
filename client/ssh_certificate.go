package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
)

// OpenSSH convention: an optional user certificate is next to the private
// key at <identity>-cert.pub. The certificate is public; the signing key stays
// in memory and is never copied into the certificate or profile store.
func identitySigners(path string, signer ssh.Signer) ([]ssh.Signer, error) {
	file, err := os.Open(path + "-cert.pub")
	if errors.Is(err, os.ErrNotExist) {
		return []ssh.Signer{signer}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 用户证书失败：%w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("读取 SSH 用户证书失败或文件超过 1 MiB")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(data)
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("SSH 用户证书格式无效")
	}
	cert, ok := key.(*ssh.Certificate)
	if !ok || cert.CertType != ssh.UserCert {
		return nil, errors.New("请选择 OpenSSH 用户证书")
	}
	certSigner, err := ssh.NewCertSigner(cert, signer)
	if err != nil {
		return nil, errors.New("SSH 用户证书与所选私钥不匹配")
	}
	// Like OpenSSH, try the certificate first and then the underlying key.
	// Server policy decides which identity is accepted.
	return []ssh.Signer{certSigner, signer}, nil
}
