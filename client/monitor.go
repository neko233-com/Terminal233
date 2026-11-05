package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Fixed, read-only Linux procfs query. No user input is interpolated.
const monitorCommand = "printf 'UP '; cat /proc/uptime; printf 'LOAD '; cat /proc/loadavg; printf 'MEM\\n'; cat /proc/meminfo"
const monitorOutputLimit = 16 << 10

type boundedMonitorOutput struct{ bytes.Buffer }

func (b *boundedMonitorOutput) Write(p []byte) (int, error) {
	if len(p) > monitorOutputLimit-b.Len() {
		return 0, errors.New("monitor output too large")
	}
	return b.Buffer.Write(p)
}

func parseMonitorOutput(raw string) (string, error) {
	var uptime, load string
	var total, available uint64
	var haveAvailable bool
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "UP":
			seconds, err := strconv.ParseFloat(fields[1], 64)
			if err != nil || math.IsNaN(seconds) || seconds < 0 || seconds > 1e12 {
				return "", errors.New("invalid uptime")
			}
			uptime = fmt.Sprintf("%.0f 小时", seconds/3600)
		case "LOAD":
			v, err := strconv.ParseFloat(fields[1], 64)
			if err != nil || math.IsNaN(v) || v < 0 || v > 1e9 {
				return "", errors.New("invalid load")
			}
			load = fmt.Sprintf("%.2f", v)
		case "MemTotal:", "MemAvailable:":
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || len(fields) < 3 || fields[2] != "kB" {
				return "", errors.New("invalid memory")
			}
			if fields[0] == "MemTotal:" {
				total = v
			} else {
				available, haveAvailable = v, true
			}
		}
	}
	if uptime == "" || load == "" || total == 0 || !haveAvailable || available > total {
		return "", errors.New("incomplete monitor output")
	}
	return fmt.Sprintf("运行时间 %s · 1 分钟负载 %s · 内存 %.1f / %.1f GiB", uptime, load, float64(total-available)/(1024*1024), float64(total)/(1024*1024)), nil
}

// Cancellation closes only this short-lived SSH channel, never the user's shell.
// A stuck channel open is abandoned once and is not retried by the poller.
func collectMonitor(ctx context.Context, client *ssh.Client) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if client == nil {
		return "", errors.New("SSH disconnected")
	}
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	var mu sync.Mutex
	var session *ssh.Session
	go func() {
		s, err := client.NewSession()
		if err != nil {
			ch <- result{"", err}
			return
		}
		mu.Lock()
		if ctx.Err() != nil {
			mu.Unlock()
			_ = s.Close()
			ch <- result{"", ctx.Err()}
			return
		}
		session = s
		mu.Unlock()
		defer s.Close()
		var output boundedMonitorOutput
		s.Stdout = &output
		if err := s.Run(monitorCommand); err != nil {
			ch <- result{"", err}
			return
		}
		text, err := parseMonitorOutput(output.String())
		ch <- result{text, err}
	}()
	select {
	case r := <-ch:
		return r.text, r.err
	case <-ctx.Done():
		mu.Lock()
		s := session
		mu.Unlock()
		if s != nil {
			_ = s.Close()
		}
		return "", ctx.Err()
	}
}

func (d *desktop) stopMonitor() {
	if d.monitorCancel != nil {
		d.monitorCancel()
		d.monitorCancel = nil
	}
	d.monitorGeneration++
	d.monitorSession, d.monitorStatus = 0, ""
}

func (d *desktop) toggleMonitor() {
	if d.monitorCancel != nil {
		d.stopMonitor()
		return
	}
	if d.shell == nil || terminalEnded(d.term) || d.win == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.monitorCancel = cancel
	d.monitorGeneration++
	generation, sessionID, shell, win := d.monitorGeneration, d.currentSession, d.shell, d.win
	d.monitorSession, d.monitorStatus = sessionID, "正在读取 Linux 服务器状态…"
	go func() {
		for {
			attempt, stop := context.WithTimeout(ctx, 8*time.Second)
			text, err := collectMonitor(attempt, shell.client)
			stop()
			if ctx.Err() != nil {
				return
			}
			win.Update(func() {
				if d.monitorGeneration != generation || d.currentSession != sessionID || d.shell != shell {
					return
				}
				if err != nil {
					d.monitorStatus = "无法读取服务器状态；仅支持提供 /proc 的 Linux 主机。"
				} else {
					d.monitorStatus = text
				}
			})
			if err != nil {
				return
			}
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
