//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestSerialFlowDCBConfiguration(t *testing.T) {
	base := serialSettings{Port: "COM3", BaudRate: 115200, DataBits: 8, Parity: "Even", StopBits: "1", DTR: true}
	for _, tc := range []struct {
		flow   string
		want   uint32
		forbid uint32
	}{
		{"RTS/CTS", serialCTSFlow | serialRTSHandshake, serialOutX | serialInX},
		{"XON/XOFF", serialOutX | serialInX, serialCTSFlow | serialRTSMask},
	} {
		t.Run(tc.flow, func(t *testing.T) {
			settings := base
			settings.Flow = tc.flow
			dcb := windows.DCB{Flags: ^uint32(0)}
			if err := applySerialFlowDCB(&dcb, settings); err != nil {
				t.Fatal(err)
			}
			if dcb.Flags&tc.want != tc.want || dcb.Flags&tc.forbid != 0 || dcb.Flags&serialDTRMask != serialDTRHigh || dcb.Flags&serialParityCheck == 0 || dcb.BaudRate != 115200 || dcb.ByteSize != 8 || dcb.XonChar != 17 || dcb.XoffChar != 19 {
				t.Fatalf("incorrect DCB for %s: %#v", tc.flow, dcb)
			}
		})
	}
}

func TestSerialFlowOverlappedIOAndCancellation(t *testing.T) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := `\\.\pipe\nekossh-serial-flow-test-` + hex.EncodeToString(nonce[:])
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	port := &serialFlowPort{handle: h}
	t.Cleanup(func() { _ = port.Close() })
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	ov := windows.Overlapped{HEvent: event}
	connectErr := windows.ConnectNamedPipe(h, &ov)
	if connectErr != nil && !errors.Is(connectErr, windows.ERROR_IO_PENDING) {
		t.Fatal(connectErr)
	}
	timeout := 3 * time.Second
	peer, err := winio.DialPipe(name, &timeout)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if errors.Is(connectErr, windows.ERROR_IO_PENDING) {
		var connected uint32
		if err := windows.GetOverlappedResult(h, &ov, &connected, true); err != nil {
			t.Fatal(err)
		}
	}
	_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
	type readResult struct {
		n    int
		err  error
		data string
	}
	result := make(chan readResult, 1)
	go func() { b := make([]byte, 5); n, err := port.Read(b); result <- readResult{n, err, string(b[:n])} }()
	if _, err := peer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.data != "hello" {
			t.Fatalf("read: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overlapped read timed out")
	}
	if n, err := port.Write([]byte("world")); err != nil || n != 5 {
		t.Fatalf("write: %d %v", n, err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(peer, buf); err != nil || string(buf) != "world" {
		t.Fatalf("peer read: %q %v", buf, err)
	}
	go func() { b := make([]byte, 5); n, err := port.Read(b); result <- readResult{n, err, string(b[:n])} }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		port.mu.Lock()
		active := port.active
		port.mu.Unlock()
		if active != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("overlapped read was not submitted")
		}
		time.Sleep(time.Millisecond)
	}
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if !errors.Is(got.err, io.ErrClosedPipe) {
			t.Fatalf("pending read not canceled: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending read did not finish after close")
	}
	if _, err := port.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after close: %v", err)
	}
}
