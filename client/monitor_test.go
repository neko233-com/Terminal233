package main

import (
	"context"
	"strings"
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
	text, err := collectMonitor(ctx, shell.client)
	if err != nil || !strings.Contains(text, "5.0 / 8.0 GiB") {
		t.Fatalf("monitor query failed: %s %v", text, err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := collectMonitor(canceled, shell.client); err == nil {
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
