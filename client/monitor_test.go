package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitorOutputValidation(t *testing.T) {
	valid := "UP 7200 0\nLOAD 1.25 0 0\nMEM\nMemTotal: 8388608 kB\nMemAvailable: 3145728 kB\n"
	text, err := parseMonitorOutput(valid)
	if err != nil || !strings.Contains(text, "2 小时") || !strings.Contains(text, "1.25") || !strings.Contains(text, "5.0 / 8.0 GiB") {
		t.Fatalf("bad metrics: %s %v", text, err)
	}
	for _, raw := range []string{"", strings.Replace(valid, "UP 7200", "UP NaN", 1), strings.Replace(valid, "LOAD 1.25", "LOAD NaN", 1), strings.Replace(valid, "MemAvailable:", "missing:", 1), strings.Replace(valid, "3145728", "9999999", 1)} {
		if _, err := parseMonitorOutput(raw); err == nil {
			t.Fatal("invalid metrics accepted")
		}
	}
	var output boundedMonitorOutput
	if _, err := output.Write(make([]byte, monitorOutputLimit+1)); err == nil {
		t.Fatal("oversized output accepted")
	}
}

func TestMonitorUsesIndependentSSHChannel(t *testing.T) {
	settings, fingerprint, _ := testSSHServerWithSlowSFTP(t, nil)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sample, err := collectMonitor(ctx, shell)
	if err != nil || !strings.Contains(sample.summary, "5.0 / 8.0 GiB") {
		t.Fatalf("monitor query failed: %s %v", sample.summary, err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := collectMonitor(canceled, shell); err == nil {
		t.Fatal("canceled monitor query succeeded")
	}
	if _, err := shell.Write([]byte("hello")); err != nil {
		t.Fatal("monitor closed the interactive shell")
	}
	buffer := make([]byte, 256)
	if _, err := shell.Read(buffer); err != nil {
		t.Fatal("interactive shell unavailable after monitoring")
	}
}

func TestMonitorLimitsPendingRequestsPerSession(t *testing.T) {
	var pause atomic.Bool
	resume := make(chan struct{})
	defer func() {
		if pause.Load() {
			close(resume)
		}
	}()
	settings, fingerprint, _ := testSSHServerWithPause(t, nil, &pause, resume)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	pause.Store(true)
	short, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	if _, err := collectMonitor(short, shell); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled request did not time out: %v", err)
	}
	if _, err := collectMonitor(context.Background(), shell); !errors.Is(err, errMonitorPending) {
		t.Fatalf("concurrent monitor was not rejected: %v", err)
	}
	pause.Store(false)
	close(resume)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, err = collectMonitor(ctx, shell)
		if err == nil {
			break
		}
		if !errors.Is(err, errMonitorPending) || ctx.Err() != nil {
			t.Fatalf("monitor slot did not recover: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMonitorRatesHandleNewAndResetInterfaces(t *testing.T) {
	base := "UP 7200 0\nLOAD 1.25 0 0\nMEM\nMemTotal: 8388608 kB\nMemAvailable: 3145728 kB\n"
	previous, err := parseMonitorSample(base + "CPU cpu 100 0 100 800 0 0 0 0\nNET\n  eth0: 1024 0 0 0 0 0 0 0 2048 0 0 0 0 0 0 0\n lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n")
	if err != nil {
		t.Fatal(err)
	}
	current, err := parseMonitorSample(base + "CPU cpu 125 0 125 850 0 0 0 0\nNET\n eth0: 6144 0 0 0 0 0 0 0 12288 0 0 0 0 0 0 0\n new0: 999999 0 0 0 0 0 0 0 999999 0 0 0 0 0 0 0\n lo: 999999 0 0 0 0 0 0 0 999999 0 0 0 0 0 0 0\n")
	if err != nil {
		t.Fatal(err)
	}
	previous.at = time.Unix(100, 0)
	current.at = previous.at.Add(5 * time.Second)
	text := formatMonitorSample(current, previous)
	if !strings.Contains(text, "CPU 50.0%") || !strings.Contains(text, "↓1.0 ↑2.0 KiB/s") {
		t.Fatalf("incorrect deltas: %s", text)
	}
	current.cpuTotal, current.network["eth0"] = 1, [2]uint64{1, 1}
	text = formatMonitorSample(current, previous)
	if strings.Contains(text, "CPU ") || strings.Contains(text, "KiB/s") {
		t.Fatalf("reset counters produced bogus rates: %s", text)
	}
	if strings.Contains(formatMonitorSample(previous, monitorSample{}), "KiB/s") {
		t.Fatal("first sample produced a rate")
	}
}

func TestMonitorRootDiskParsingAndMissingDisk(t *testing.T) {
	base := "UP 7200 0\nLOAD 1.25 0 0\nMEM\nMemTotal: 8388608 kB\nMemAvailable: 3145728 kB\nNET\n"
	valid := base + "DISK\nFilesystem 1024-blocks Used Available Capacity Mounted on\n/dev/test 10485760 3145728 7340032 30% /\n"
	sample, err := parseMonitorSample(valid)
	if err != nil || !strings.Contains(sample.summary, "根磁盘 3.0 / 10.0 GiB") {
		t.Fatalf("disk not parsed: %s %v", sample.summary, err)
	}
	for _, input := range []string{base, base + "DISK\nno df available\n", strings.Replace(valid, "3145728 7340032", "99999999 0", 1), strings.Replace(valid, "30% /", "30% /other", 1)} {
		sample, err = parseMonitorSample(input)
		if err != nil || strings.Contains(sample.summary, "根磁盘") {
			t.Fatalf("invalid or missing disk should be omitted: %s %v", sample.summary, err)
		}
	}
}

func TestMonitorStopsOnSessionSwitchAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &desktop{currentSession: 1, monitorSession: 1, monitorCancel: cancel, monitorGeneration: 3, monitorStatus: "old", sessions: []*workspaceSession{{id: 1}, {id: 2}}}
	d.activateSession(2)
	if ctx.Err() == nil || d.monitorCancel != nil || d.monitorStatus != "" || d.monitorGeneration != 4 {
		t.Fatal("switch did not cancel the old monitor")
	}
	ctx, cancel = context.WithCancel(context.Background())
	d.monitorCancel, d.monitorSession = cancel, 2
	d.closeSession(2)
	if ctx.Err() == nil || d.monitorCancel != nil || d.monitorSession != 0 {
		t.Fatal("closing session did not cancel monitoring")
	}
}
