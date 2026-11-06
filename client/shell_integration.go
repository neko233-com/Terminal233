package main

import (
	"bytes"
	"encoding/hex"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// shellIntegration observes the terminal output without altering it. Only
// explicit shell-integration markers affect state; keyboard input is never
// collected, logged, or persisted here.
type shellIntegration struct {
	mu               sync.Mutex
	state            uint8
	seq              []byte
	overflow         bool
	phase            shellPhase
	pending, command string
	promptSerial     uint64
}
type shellPhase uint8

const (
	shellUnknown shellPhase = iota
	shellPromptStart
	shellPrompt
	shellRunning
	shellFinished
)
const maxShellSequence = 8192

// feed supports fragmented OSC sequences (BEL or ST terminated) and ignores
// marker-shaped bytes inside other control strings. Output is never consumed.
func (s *shellIntegration) feed(data []byte) {
	for len(data) > 0 {
		n := s.feedUntilPrompt(data)
		data = data[n:]
	}
}

// Stop at a prompt marker so the terminal can capture its exact cursor before
// processing any following output from the same SSH read.
func (s *shellIntegration) feedUntilPrompt(data []byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < len(data); {
		if s.state == 0 {
			index := bytes.IndexByte(data[i:], 0x1b)
			if index < 0 {
				break
			}
			i += index + 1
			s.state = 1
			continue
		}
		b := data[i]
		i++
		switch s.state {
		case 1:
			switch b {
			case ']':
				s.state = 2
				s.resetSequence()
			case 'P', 'X', '^', '_':
				s.state = 4
			case 0x1b:
				s.state = 1
			default:
				s.state = 0
			}
		case 2:
			switch b {
			case 0x07:
				boundary := !s.overflow && s.accept(string(s.seq))
				s.state = 0
				s.resetSequence()
				if boundary {
					return i
				}
			case 0x1b:
				s.state = 3
			case 0x18, 0x1a:
				s.state = 0
				s.resetSequence()
			default:
				if !s.overflow && len(s.seq) < maxShellSequence {
					s.seq = append(s.seq, b)
					if len(s.seq) == 4 && string(s.seq) != "133;" && string(s.seq) != "633;" {
						clear(s.seq)
						s.seq = s.seq[:0]
						s.overflow = true
					}
				} else {
					s.overflow = true
				}
			}
		case 3:
			if b == '\\' {
				boundary := !s.overflow && s.accept(string(s.seq))
				s.state = 0
				s.resetSequence()
				if boundary {
					return i
				}
			} else {
				s.state = 0
				s.resetSequence()
				if b == 0x1b {
					s.state = 1
				}
			}
		case 4:
			if b == 0x1b {
				s.state = 5
			} else if b == 0x18 || b == 0x1a {
				s.state = 0
			}
		case 5:
			if b == '\\' {
				s.state = 0
			} else if b != 0x1b {
				s.state = 4
			}
		}
	}
	return len(data)
}
func (s *shellIntegration) resetSequence() {
	clear(s.seq)
	s.seq = s.seq[:0]
	s.overflow = false
}
func (s *shellIntegration) accept(value string) bool {
	prefix, rest, ok := strings.Cut(value, ";")
	if !ok || (prefix != "133" && prefix != "633") {
		return false
	}
	kind, payload, _ := strings.Cut(rest, ";")
	switch kind {
	case "A":
		s.phase = shellPromptStart
		s.pending = ""
		s.command = ""
	case "B":
		s.phase = shellPrompt
		s.promptSerial++
		if s.promptSerial == 0 {
			s.promptSerial++
		}
		return true
	case "C":
		s.phase = shellRunning
		s.command = s.pending
		s.pending = ""
	case "D":
		s.phase = shellFinished
		s.pending = ""
		s.command = ""
	case "E":
		if prefix != "633" {
			return false
		}
		encoded, _, _ := strings.Cut(payload, ";")
		command, ok := decodeShellCommand(encoded)
		if ok {
			s.pending = command
		} else {
			s.pending = ""
		}
	}
	return false
}
func decodeShellCommand(encoded string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(encoded); i++ {
		if encoded[i] != '\\' {
			out.WriteByte(encoded[i])
			continue
		}
		i++
		if i >= len(encoded) {
			return "", false
		}
		if encoded[i] == '\\' {
			out.WriteByte('\\')
			continue
		}
		if encoded[i] != 'x' || i+2 >= len(encoded) {
			return "", false
		}
		b, err := hex.DecodeString(encoded[i+1 : i+3])
		if err != nil {
			return "", false
		}
		out.WriteByte(b[0])
		i += 2
	}
	result := strings.TrimSpace(out.String())
	if len(result) > 4096 || !utf8.ValidString(result) || strings.IndexFunc(result, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "", false
	}
	return result, true
}
func (s *shellIntegration) promptState() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptSerial, s.phase == shellPrompt
}
func (s *shellIntegration) snapshot() (shellPhase, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.command
}
func sessionRunningCommand(s *workspaceSession) string {
	if s == nil || s.shell == nil || s.term == nil || terminalEnded(s.term) {
		return ""
	}
	phase, command := s.shell.integration.snapshot()
	if phase != shellRunning {
		return ""
	}
	if command == "" {
		return "命令运行中"
	}
	return command
}
