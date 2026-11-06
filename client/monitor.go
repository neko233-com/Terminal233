package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Fixed, read-only Linux procfs query. No user input is interpolated.
const monitorCommand = "printf 'UP '; cat /proc/uptime; printf 'LOAD '; cat /proc/loadavg; printf 'MEM\\n'; cat /proc/meminfo; printf 'CPU '; IFS= read -r cpu_line </proc/stat && printf '%s\\n' \"$cpu_line\"; printf 'NET\\n'; cat /proc/net/dev 2>/dev/null || :; printf 'DISK\\n'; LC_ALL=C df -Pk / 2>/dev/null || :"
const monitorOutputLimit = 64 << 10

var errMonitorPending = errors.New("previous monitor request is still running")

type monitorSample struct {
	summary                 string
	cpuTotal, cpuIdle       uint64
	cpuOK                   bool
	memoryUsed, memoryTotal uint64
	diskUsed, diskTotal     uint64
	network                 map[string][2]uint64
	at                      time.Time
}

func parseMonitorSample(raw string) (monitorSample, error) {
	summary, err := parseMonitorOutput(raw)
	if err != nil {
		return monitorSample{}, err
	}
	sample := monitorSample{summary: summary, network: make(map[string][2]uint64)}
	inNetwork, inDisk, diskFound := false, false, false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[2] == "kB" {
			if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				switch fields[0] {
				case "MemTotal:":
					sample.memoryTotal = v
				case "MemAvailable:":
					sample.memoryUsed = v
				}
			}
		}
		if strings.TrimSpace(line) == "DISK" {
			inNetwork, inDisk = false, true
			continue
		}
		if inDisk {
			fields := strings.Fields(line)
			if !diskFound && len(fields) >= 6 && fields[len(fields)-1] == "/" {
				total, totalErr := strconv.ParseUint(fields[len(fields)-5], 10, 64)
				used, usedErr := strconv.ParseUint(fields[len(fields)-4], 10, 64)
				if totalErr == nil && usedErr == nil && total > 0 && used <= total {
					sample.diskTotal, sample.diskUsed = total, used
					sample.summary += fmt.Sprintf(" · 根磁盘 %.1f / %.1f GiB", float64(used)/(1024*1024), float64(total)/(1024*1024))
					diskFound = true
				}
			}
			continue
		}
		if strings.TrimSpace(line) == "NET" {
			inNetwork = true
			continue
		}
		if strings.HasPrefix(line, "CPU cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}
			var total, idle uint64
			valid := true
			for i, field := range fields[2:min(len(fields), 10)] {
				v, e := strconv.ParseUint(field, 10, 64)
				var carry uint64
				total, carry = bits.Add64(total, v, 0)
				if e != nil || carry != 0 {
					valid = false
					break
				}
				if i == 3 || i == 4 {
					idle, carry = bits.Add64(idle, v, 0)
					if carry != 0 {
						valid = false
						break
					}
				}
			}
			if valid && total > 0 && idle <= total {
				sample.cpuTotal, sample.cpuIdle, sample.cpuOK = total, idle, true
			}
			continue
		}
		if !inNetwork {
			continue
		}
		name, values, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		fields = strings.Fields(values)
		if !found || name == "" || name == "lo" || len(fields) < 16 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil && txErr == nil {
			sample.network[name] = [2]uint64{rx, tx}
		}
	}
	if sample.memoryTotal > 0 && sample.memoryUsed <= sample.memoryTotal {
		sample.memoryUsed = sample.memoryTotal - sample.memoryUsed
	}
	return sample, nil
}

type monitorDisplay struct {
	text                       string
	cpu, memory, disk          float64
	hasCPU, hasMemory, hasDisk bool
}

func formatMonitorSample(current, previous monitorSample) string {
	return makeMonitorDisplay(current, previous).text
}

func makeMonitorDisplay(current, previous monitorSample) monitorDisplay {
	display := monitorDisplay{text: current.summary}
	if current.memoryTotal > 0 && current.memoryUsed <= current.memoryTotal {
		display.memory, display.hasMemory = float64(current.memoryUsed)/float64(current.memoryTotal), true
	}
	if current.diskTotal > 0 && current.diskUsed <= current.diskTotal {
		display.disk, display.hasDisk = float64(current.diskUsed)/float64(current.diskTotal), true
	}
	if current.cpuOK && previous.cpuOK && current.cpuTotal > previous.cpuTotal && current.cpuIdle >= previous.cpuIdle {
		total, idle := current.cpuTotal-previous.cpuTotal, current.cpuIdle-previous.cpuIdle
		if idle <= total {
			display.cpu, display.hasCPU = float64(total-idle)/float64(total), true
			display.text += fmt.Sprintf(" · CPU %.1f%%", 100*display.cpu)
		}
	}
	seconds := current.at.Sub(previous.at).Seconds()
	if seconds > 0 && !previous.at.IsZero() {
		var rx, tx uint64
		count := 0
		valid := true
		for name, now := range current.network {
			old, ok := previous.network[name]
			if !ok || now[0] < old[0] || now[1] < old[1] {
				continue
			}
			var carry uint64
			rx, carry = bits.Add64(rx, now[0]-old[0], 0)
			if carry != 0 {
				valid = false
				break
			}
			tx, carry = bits.Add64(tx, now[1]-old[1], 0)
			if carry != 0 {
				valid = false
				break
			}
			count++
		}
		if valid && count > 0 {
			display.text += fmt.Sprintf(" · 网卡合计 ↓%.1f ↑%.1f KiB/s", float64(rx)/seconds/1024, float64(tx)/seconds/1024)
		}
	}
	return display
}

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
func collectMonitor(ctx context.Context, shell *remoteShell) (monitorSample, error) {
	if err := ctx.Err(); err != nil {
		return monitorSample{}, err
	}
	if shell == nil || shell.client == nil {
		return monitorSample{}, errors.New("SSH disconnected")
	}
	if !shell.takeMonitorSlot() {
		return monitorSample{}, errMonitorPending
	}
	type result struct {
		sample monitorSample
		err    error
	}
	ch := make(chan result, 1)
	var mu sync.Mutex
	var session *ssh.Session
	go func() {
		run := func() result {
			s, err := shell.client.NewSession()
			if err != nil {
				return result{monitorSample{}, err}
			}
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				_ = s.Close()
				return result{monitorSample{}, ctx.Err()}
			}
			session = s
			mu.Unlock()
			defer s.Close()
			var output boundedMonitorOutput
			s.Stdout = &output
			if err := s.Run(monitorCommand); err != nil {
				return result{monitorSample{}, err}
			}
			sample, err := parseMonitorSample(output.String())
			sample.at = time.Now()
			return result{sample, err}
		}
		r := run()
		<-shell.monitorGate
		ch <- r
	}()
	select {
	case r := <-ch:
		return r.sample, r.err
	case <-ctx.Done():
		mu.Lock()
		s := session
		mu.Unlock()
		if s != nil {
			_ = s.Close()
		}
		return monitorSample{}, ctx.Err()
	}
}

func (d *desktop) stopMonitor() {
	if d.monitorCancel != nil {
		d.monitorCancel()
		d.monitorCancel = nil
	}
	d.monitorGeneration++
	d.monitorSession, d.monitorStatus, d.monitorData = 0, "", monitorDisplay{}
}

func (d *desktop) toggleMonitor() {
	if d.monitorCancel != nil {
		d.stopMonitor()
		return
	}
	if d.shell == nil || d.term == nil || terminalEnded(d.term) || d.win == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.monitorCancel = cancel
	d.monitorGeneration++
	generation, sessionID, shell, win := d.monitorGeneration, d.currentSession, d.shell, d.win
	d.monitorSession, d.monitorStatus = sessionID, "正在读取 Linux 服务器状态…"
	go func() {
		var previous monitorSample
		for {
			attempt, stop := context.WithTimeout(ctx, 8*time.Second)
			sample, err := collectMonitor(attempt, shell)
			stop()
			if ctx.Err() != nil {
				return
			}
			display := makeMonitorDisplay(sample, previous)
			previous = sample
			win.Update(func() {
				if d.monitorGeneration != generation || d.currentSession != sessionID || d.shell != shell {
					return
				}
				if errors.Is(err, errMonitorPending) {
					d.monitorData = monitorDisplay{}
					d.monitorStatus = "上次服务器状态请求尚未结束，请稍后关闭并重试。"
				} else if err != nil {
					d.monitorData = monitorDisplay{}
					d.monitorStatus = "无法读取服务器状态；仅支持提供 /proc 的 Linux 主机。"
				} else {
					d.monitorData, d.monitorStatus = display, display.text
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
