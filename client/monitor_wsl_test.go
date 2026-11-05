//go:build windows && linuxprobe

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This optional local check runs the exact read-only probe inside a named WSL
// distribution. It does not open an SSH connection or alter that distribution.
func TestMonitorCommandOnWSL(t *testing.T) {
	distro := os.Getenv("NEKOSSH_LINUX_PROBE_DISTRO")
	if distro == "" {
		t.Skip("set NEKOSSH_LINUX_PROBE_DISTRO to opt in")
	}
	var previous monitorSample
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, "wsl.exe", "-d", distro, "-u", "root", "--", "/bin/sh", "-s")
		cmd.Stdin = strings.NewReader(monitorCommand)
		output, err := cmd.Output()
		cancel()
		if err != nil {
			t.Fatalf("Linux probe failed: %v", err)
		}
		if len(output) > monitorOutputLimit {
			t.Fatal("Linux probe output exceeded the limit")
		}
		sample, err := parseMonitorSample(string(output))
		if err != nil || !sample.cpuOK || len(sample.network) == 0 {
			for _, line := range strings.Split(string(output), "\n") {
				if strings.HasPrefix(line, "CPU") {
					t.Logf("CPU line: %q", line)
				}
			}
			t.Fatalf("Linux probe output is incomplete: cpu=%t network=%d error=%v", sample.cpuOK, len(sample.network), err)
		}
		sample.at = time.Now()
		if i > 0 {
			text := formatMonitorSample(sample, previous)
			if !strings.Contains(text, "CPU ") || !strings.Contains(text, "KiB/s") {
				t.Fatalf("Linux samples did not produce rates: %s", text)
			}
		}
		previous = sample
	}
}
