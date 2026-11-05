package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func testSSHServer(t *testing.T, authorized ...ssh.PublicKey) (connectionSettings, string, *atomic.Int32) {
	return testSSHServerWithSlowSFTP(t, nil, authorized...)
}

type slowSFTPStream struct {
	io.ReadWriteCloser
	slow *atomic.Bool
}

func (s *slowSFTPStream) Read(p []byte) (int, error) {
	if s.slow.Load() {
		time.Sleep(25 * time.Millisecond)
	}
	return s.ReadWriteCloser.Read(p)
}

func (s *slowSFTPStream) Write(p []byte) (int, error) {
	if s.slow.Load() {
		time.Sleep(25 * time.Millisecond)
	}
	return s.ReadWriteCloser.Write(p)
}

func testSSHServerWithSlowSFTP(t *testing.T, slow *atomic.Bool, authorized ...ssh.PublicKey) (connectionSettings, string, *atomic.Int32) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	attempts := &atomic.Int32{}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		attempts.Add(1)
		if string(password) != "test-password" {
			return nil, ssh.ErrNoAuth
		}
		return nil, nil
	}}
	if len(authorized) > 0 {
		config.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			attempts.Add(1)
			if ssh.FingerprintSHA256(key) != ssh.FingerprintSHA256(authorized[0]) {
				return nil, ssh.ErrNoAuth
			}
			return nil, nil
		}
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					if ch.ChannelType() == "direct-tcpip" {
						var request struct {
							Host       string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if ssh.Unmarshal(ch.ExtraData(), &request) != nil || request.Host != "127.0.0.1" || request.Port > 65535 {
							_ = ch.Reject(ssh.Prohibited, "loopback test targets only")
							continue
						}
						target, dialErr := net.DialTimeout("tcp", net.JoinHostPort(request.Host, fmt.Sprint(request.Port)), time.Second)
						if dialErr != nil {
							_ = ch.Reject(ssh.ConnectionFailed, "test target unavailable")
							continue
						}
						channel, requests, acceptErr := ch.Accept()
						if acceptErr != nil {
							_ = target.Close()
							continue
						}
						go ssh.DiscardRequests(requests)
						go func() {
							defer target.Close()
							defer channel.Close()
							go io.Copy(target, channel)
							_, _ = io.Copy(channel, target)
						}()
						continue
					}
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							switch req.Type {
							case "pty-req", "window-change":
								_ = req.Reply(true, nil)
							case "subsystem":
								var sub struct{ Name string }
								if ssh.Unmarshal(req.Payload, &sub) != nil || sub.Name != "sftp" {
									_ = req.Reply(false, nil)
									continue
								}
								_ = req.Reply(true, nil)
								var stream io.ReadWriteCloser = channel
								if slow != nil {
									stream = &slowSFTPStream{ReadWriteCloser: channel, slow: slow}
								}
								srv, err := sftp.NewServer(stream)
								if err == nil {
									_ = srv.Serve()
									_ = srv.Close()
								}
								return
							case "shell":
								_ = req.Reply(true, nil)
								_, _ = channel.Write([]byte("ready\r\n"))
								buffer := make([]byte, 1024)
								var seen string
								for {
									n, readErr := channel.Read(buffer)
									if n > 0 {
										seen += string(buffer[:n])
										if strings.Contains(seen, "__NEKOSSH_TEST_DISCONNECT__") {
											return
										}
										if len(seen) > 128 {
											seen = seen[len(seen)-128:]
										}
										_, _ = channel.Write(buffer[:n])
									}
									if readErr != nil {
										return
									}
								}
							default:
								_ = req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	return connectionSettings{Host: host, Port: port, User: "alice", Password: "test-password"}, ssh.FingerprintSHA256(signer.PublicKey()), attempts
}

func TestHostConfirmationAndShell(t *testing.T) {
	settings, expected, attempts := testSSHServer(t)
	fingerprint, err := inspectHost(settings)
	if err != nil || fingerprint != expected {
		t.Fatalf("fingerprint=%q err=%v", fingerprint, err)
	}
	if attempts.Load() != 0 {
		t.Fatal("host inspection sent credentials")
	}
	settings.Fingerprint = "SHA256:incorrect"
	if shell, err := connectShell(settings); err == nil {
		_ = shell.Close()
		t.Fatal("accepted a changed host key")
	}
	if attempts.Load() != 0 {
		t.Fatal("sent credentials to a host with a mismatched key")
	}
	settings.Fingerprint = expected
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	result := make(chan string, 1)
	go func() { buf := make([]byte, 64); n, _ := shell.Read(buf); result <- string(buf[:n]) }()
	select {
	case got := <-result:
		if !strings.Contains(got, "ready") {
			t.Fatalf("unexpected output %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shell output timed out")
	}
	if attempts.Load() != 1 {
		t.Fatalf("authentication attempts=%d", attempts.Load())
	}
}

func TestInvalidConnectionSettings(t *testing.T) {
	for _, input := range []connectionSettings{{Host: "example.com", Port: "0", User: "a"}, {Host: "example.com/path", Port: "22", User: "a"}, {Host: "example.com", Port: "22"}} {
		if _, err := input.address(); err == nil {
			t.Fatalf("accepted invalid settings: %+v", input)
		}
	}
}

func TestSFTPTransferAndExplicitDelete(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.ToSlash(filepath.Join(root, "remote"))
	if err := os.Mkdir(filepath.Join(root, "remote"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := uploadFile(shell, source, remoteDir); err != nil {
		t.Fatal(err)
	}
	if err := uploadFile(shell, source, remoteDir); err == nil {
		t.Fatal("upload overwrote existing file")
	}
	files, err := listRemote(shell, remoteDir)
	if err != nil || len(files) != 1 || files[0].Name != "source.txt" || files[0].Size != 5 || files[0].Modified <= 0 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	target := filepath.Join(root, "download.txt")
	if err := downloadFile(shell, path.Join(remoteDir, "source.txt"), target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "hello" {
		t.Fatalf("download=%q err=%v", data, err)
	}
	if err := downloadFile(shell, path.Join(remoteDir, "source.txt"), target); err == nil {
		t.Fatal("download overwrote local file")
	}
	if err := os.WriteFile(filepath.Join(root, "remote", "existing.txt"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := renameFile(shell, remoteDir, "source.txt", "existing.txt"); err == nil {
		t.Fatal("remote rename replaced existing file")
	}
	if err := renameFile(shell, remoteDir, "source.txt", "../escape"); err == nil {
		t.Fatal("remote rename allowed traversal")
	}
	if err := renameFile(shell, remoteDir, "source.txt", "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "remote", "existing.txt")); err != nil || string(got) != "protected" {
		t.Fatal("existing remote target changed")
	}
	if err := renameFile(shell, remoteDir, "renamed.txt", "source.txt"); err != nil {
		t.Fatal(err)
	}
	if err := deleteRemote(shell, path.Join(remoteDir, "source.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "remote", "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
	if err := makeDirectory(shell, remoteDir, "新目录"); err != nil {
		t.Fatal(err)
	}
	if err := makeDirectory(shell, remoteDir, "新目录"); err == nil {
		t.Fatal("existing remote directory was replaced")
	}
	if err := makeDirectory(shell, remoteDir, "../outside"); err == nil {
		t.Fatal("remote traversal accepted")
	}
	if info, err := os.Stat(filepath.Join(root, "remote", "新目录")); err != nil || !info.IsDir() {
		t.Fatal("remote directory was not created")
	}
}

func TestSFTPStreamsLargeFilesAndCleansCancelledTransfers(t *testing.T) {
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	root := t.TempDir()
	source := filepath.Join(root, "large.bin")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(17 << 20); err == nil {
		_, err = f.WriteAt([]byte("end"), (17<<20)-3)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.Join(root, "remote")
	if err = os.Mkdir(remoteDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = uploadFile(shell, source, filepath.ToSlash(remoteDir)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "download.bin")
	if err = downloadFile(shell, path.Join(filepath.ToSlash(remoteDir), "large.bin"), target); err != nil {
		t.Fatal(err)
	}
	hashFile := func(name string) [32]byte {
		t.Helper()
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			t.Fatal(err)
		}
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		return sum
	}
	if hashFile(source) != hashFile(target) {
		t.Fatal("large transfer changed file contents")
	}
	localCopy := filepath.Join(root, "copy.bin")
	if err = copyLocalFile(source, localCopy); err != nil || hashFile(source) != hashFile(localCopy) {
		t.Fatalf("large local copy failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledSource := filepath.Join(root, "cancelled-upload.bin")
	if err = os.WriteFile(cancelledSource, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = uploadFileContext(ctx, shell, cancelledSource, filepath.ToSlash(remoteDir), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload returned %v", err)
	}
	if _, err = os.Stat(filepath.Join(remoteDir, "cancelled-upload.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled upload left a partial file: %v", err)
	}
	cancelledTarget := filepath.Join(root, "cancelled.bin")
	if err = downloadFileContext(ctx, shell, path.Join(filepath.ToSlash(remoteDir), "large.bin"), cancelledTarget, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled download returned %v", err)
	}
	if _, err = os.Stat(cancelledTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled download left a partial file: %v", err)
	}
}
