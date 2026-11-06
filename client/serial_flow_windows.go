//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DCB flag bits, as defined by Windows winbase.h.
const (
	serialBinary       = uint32(1 << 0)
	serialParityCheck  = uint32(1 << 1)
	serialCTSFlow      = uint32(1 << 2)
	serialDSRFlow      = uint32(1 << 3)
	serialDTRMask      = uint32(3 << 4)
	serialDTRHigh      = uint32(1 << 4)
	serialDSRSensitive = uint32(1 << 6)
	serialContinueXoff = uint32(1 << 7)
	serialOutX         = uint32(1 << 8)
	serialInX          = uint32(1 << 9)
	serialErrorChar    = uint32(1 << 10)
	serialNull         = uint32(1 << 11)
	serialRTSMask      = uint32(3 << 12)
	serialRTSHigh      = uint32(1 << 12)
	serialRTSHandshake = uint32(2 << 12)
	serialAbortOnError = uint32(1 << 14)
)

func applySerialFlowDCB(dcb *windows.DCB, s serialSettings) error {
	if err := s.validate(); err != nil {
		return err
	}
	dcb.DCBlength = uint32(unsafe.Sizeof(*dcb))
	dcb.BaudRate, dcb.ByteSize = uint32(s.BaudRate), byte(s.DataBits)
	dcb.Parity = map[string]byte{"None": windows.NOPARITY, "Odd": windows.ODDPARITY, "Even": windows.EVENPARITY, "Mark": windows.MARKPARITY, "Space": windows.SPACEPARITY}[s.Parity]
	dcb.StopBits = map[string]byte{"1": windows.ONESTOPBIT, "1.5": windows.ONE5STOPBITS, "2": windows.TWOSTOPBITS}[s.StopBits]
	dcb.Flags &^= serialParityCheck | serialCTSFlow | serialDSRFlow | serialDTRMask | serialDSRSensitive | serialContinueXoff | serialOutX | serialInX | serialErrorChar | serialNull | serialRTSMask | serialAbortOnError
	dcb.Flags |= serialBinary
	if s.Parity != "None" {
		dcb.Flags |= serialParityCheck
	}
	if s.DTR {
		dcb.Flags |= serialDTRHigh
	}
	if s.Flow == "RTS/CTS" {
		dcb.Flags |= serialCTSFlow | serialRTSHandshake
	} else if s.RTS {
		dcb.Flags |= serialRTSHigh
	}
	if s.Flow == "XON/XOFF" {
		dcb.Flags |= serialOutX | serialInX
	}
	dcb.XonLim, dcb.XoffLim, dcb.XonChar, dcb.XoffChar = 2048, 512, 17, 19
	return nil
}

func openSerialFlowPort(settings serialSettings) (io.ReadWriteCloser, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(`\\.\` + settings.Port)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, errors.New("设备已被占用或没有访问权限")
		}
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, errors.New("没有找到串口设备")
		}
		return nil, err
	}
	closeOnError := func(err error) (io.ReadWriteCloser, error) { _ = windows.CloseHandle(h); return nil, err }
	var dcb windows.DCB
	dcb.DCBlength = uint32(unsafe.Sizeof(dcb))
	if err = windows.GetCommState(h, &dcb); err != nil {
		return closeOnError(fmt.Errorf("读取串口设置失败：%w", err))
	}
	if err = applySerialFlowDCB(&dcb, settings); err != nil {
		return closeOnError(err)
	}
	if err = windows.SetCommState(h, &dcb); err != nil {
		return closeOnError(fmt.Errorf("设备不支持串口设置：%w", err))
	}
	// Keep reads blocking until data arrives while accommodating common USB
	// serial drivers that mishandle the usual MAXDWORD-1 timeout value.
	timeouts := windows.CommTimeouts{ReadIntervalTimeout: 0xffffffff, ReadTotalTimeoutMultiplier: 0xffffffff, ReadTotalTimeoutConstant: 0x7ffffffe}
	if err = windows.SetCommTimeouts(h, &timeouts); err != nil {
		return closeOnError(fmt.Errorf("设置串口读取超时失败：%w", err))
	}
	return &serialFlowPort{handle: h}, nil
}

// The lock covers issuing each overlapped request, so Close cannot cancel
// before a request is submitted. The event and buffers remain alive until its
// GetOverlappedResult has completed even if a device driver ignores cancel.
type serialFlowPort struct {
	mu      sync.Mutex
	handle  windows.Handle
	closed  bool
	active  int
	pending sync.WaitGroup
}

func (p *serialFlowPort) transfer(buf []byte, read bool) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	var n uint32
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	p.pending.Add(1)
	p.active++
	h := p.handle
	if read {
		err = windows.ReadFile(h, buf, &n, &overlapped)
	} else {
		err = windows.WriteFile(h, buf, &n, &overlapped)
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
		p.pending.Done()
	}()
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		err = windows.GetOverlappedResult(h, &overlapped, &n, true)
	}
	runtime.KeepAlive(buf)
	if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		err = io.ErrClosedPipe
	}
	return int(n), err
}

func (p *serialFlowPort) Read(buf []byte) (int, error) {
	for {
		n, err := p.transfer(buf, true)
		if n != 0 || err != nil || len(buf) == 0 {
			return n, err
		}
	}
}

func (p *serialFlowPort) Write(buf []byte) (int, error) { return p.transfer(buf, false) }

func (p *serialFlowPort) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	h := p.handle
	_ = windows.CancelIoEx(h, nil)
	p.mu.Unlock()
	done := make(chan struct{})
	go func() { p.pending.Wait(); _ = windows.CloseHandle(h); close(done) }()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		// A driver can ignore cancellation; do not block the UI or release
		// an OVERLAPPED structure while the kernel may still use it.
	}
	return nil
}
