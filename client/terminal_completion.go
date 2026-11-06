package main

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

type completionTerminal interface {
	NekoSetCompletionKey(func(ui.Modifiers, ui.Key) bool)
	NekoPromptLine() (string, int, int, int, int, bool)
	NekoCaretOffset() (float32, float32, float32, bool)
}
type completionItem struct {
	value, label string
	replace      bool
}
type terminalCompletion struct {
	prefix, dismissed  string
	items              []completionItem
	selected           int
	visible            bool
	serial             uint64
	pending, uncertain bool
	waitingText        int
	expectedHashes     [][32]byte
}

// Track unacknowledged input without retaining its plaintext. The hashes are
// short-lived echo guards, never persisted or used as a credential store.
func completionHashAppend(previous [32]byte, r rune) [32]byte {
	var input [36]byte
	copy(input[:32], previous[:])
	n := utf8.EncodeRune(input[32:], r)
	return sha256.Sum256(input[:32+n])
}
func completionHashes(text string) [][32]byte {
	hashes := make([][32]byte, 1, utf8.RuneCountInString(text)+1)
	for _, r := range text {
		hashes = append(hashes, completionHashAppend(hashes[len(hashes)-1], r))
	}
	return hashes
}
func (state *terminalCompletion) clearPending() {
	clear(state.expectedHashes)
	state.expectedHashes = nil
	state.pending = false
	state.waitingText = 0
}
func (state *terminalCompletion) invalidate() {
	state.clearPending()
	state.visible = false
	state.uncertain = true
	state.prefix = ""
	state.dismissed = ""
	state.items = nil
}

var shellCompletionCommands = []string{
	"cd ", "ls", "ls -la", "pwd", "cat ", "less ", "tail -f ", "grep ",
	"git status", "git log", "git diff", "git pull", "git push", "git branch", "git checkout ",
	"docker ps", "docker logs ", "docker compose ", "kubectl get ", "kubectl logs ",
	"systemctl status ", "journalctl -f", "mkdir ", "find ", "echo ", "exit",
}

func completionCandidates(prefix string, snippets []quickCommand) []completionItem {
	if prefix == "" || len(prefix) > 256 || !utf8.ValidString(prefix) || strings.IndexFunc(prefix, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return nil
	}
	result := make([]completionItem, 0, 5)
	seen := make(map[string]bool)
	// Replacing a search term uses the remote shell's Backspace binding. Avoid
	// splitting combining-character sequences whose editing behavior is ambiguous.
	canReplace := strings.IndexFunc(prefix, func(r rune) bool { return unicode.Is(unicode.M, r) }) < 0
	add := func(value, label string, replace bool) {
		if len(result) >= 5 || strings.TrimSpace(value) == "" || len(value) > 512 || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\t") || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 || value == prefix || seen[value] || (replace && !canReplace) {
			return
		}
		seen[value] = true
		result = append(result, completionItem{value: value, label: label, replace: replace})
	}
	query := strings.ToLower(prefix)
	for _, snippet := range snippets {
		if !utf8.ValidString(snippet.Name) || strings.IndexFunc(snippet.Name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
			continue
		}
		if strings.HasPrefix(snippet.Command, prefix) {
			add(snippet.Command, snippet.Name, false)
			continue
		}
		if strings.Contains(strings.ToLower(snippet.Name), query) || strings.Contains(strings.ToLower(snippet.Command), query) {
			add(snippet.Command, snippet.Name, true)
		}
	}
	for _, command := range shellCompletionCommands {
		if strings.HasPrefix(command, prefix) {
			add(command, "", false)
		}
	}
	return result
}

func (d *desktop) completionAllowed(s *workspaceSession) bool {
	if d.autocompleteDisabled || d.workspace != "terminal" || s == nil || s.id != d.currentSession || s.shell == nil || s.term == nil || terminalEnded(s.term) {
		return false
	}
	if g := d.terminalGroupFor(s.id); g != nil && g.broadcast {
		return false
	}
	return true
}
func (d *desktop) bindTerminalCompletion(s *workspaceSession) {
	if s == nil || s.term == nil {
		return
	}
	source := s.term
	if hook, ok := any(source).(completionTerminal); ok {
		hook.NekoSetCompletionKey(func(mods ui.Modifiers, key ui.Key) bool {
			if s.term != source {
				return false
			}
			return d.handleCompletionKey(s, mods, key)
		})
	}
}
func (d *desktop) completionCurrent(s *workspaceSession) (string, completionTerminal, bool) {
	if !d.completionAllowed(s) {
		return "", nil, false
	}
	t, ok := any(s.term).(completionTerminal)
	if !ok {
		return "", nil, false
	}
	serial, active := s.shell.integration.promptState()
	if !active {
		return "", nil, false
	}
	prefix, _, _, _, _, ok := t.NekoPromptLine()
	nextSerial, stillActive := s.shell.integration.promptState()
	if !ok || !stillActive || serial != nextSerial {
		return "", nil, false
	}
	if s.completion.serial != serial {
		s.completion.clearPending()
		s.completion = terminalCompletion{serial: serial}
	}
	return prefix, t, true
}

func completionPrintableKey(key ui.Key) bool {
	return key >= ui.KeyA && key <= ui.KeyZ || key >= ui.Key0 && key <= ui.Key9 || key == ui.KeySpace || key == ui.KeyMinus || key == ui.KeyEqual || key == ui.KeyComma || key == ui.KeyPeriod || key == ui.KeySlash || key == ui.KeySemicolon || key == ui.KeyQuote || key == ui.KeyBracketLeft || key == ui.KeyBracketRight || key == ui.KeyBackslash || key == ui.KeyBackquote
}

// Do not use a displayed prefix for completion while an input event is still
// in flight to the remote shell. Match the echo to the expected visible line.
func (d *desktop) noteCompletionKey(s *workspaceSession, mods ui.Modifiers, key ui.Key) {
	prefix, _, ok := d.completionCurrent(s)
	if !ok {
		s.completion.visible = false
		return
	}
	state := &s.completion
	state.visible = false
	if state.uncertain {
		return
	}
	if !state.pending {
		state.pending, state.expectedHashes = true, completionHashes(prefix)
	}
	if mods & ^ui.Shift != 0 {
		state.invalidate()
		return
	}
	if completionPrintableKey(key) {
		state.waitingText++
		return
	}
	if key == ui.KeyBackspace && mods == 0 && state.waitingText == 0 {
		if len(state.expectedHashes) > 1 {
			last := len(state.expectedHashes) - 1
			state.expectedHashes[last] = [32]byte{}
			state.expectedHashes = state.expectedHashes[:last]
		}
		return
	}
	state.invalidate()
}
func (d *desktop) noteCompletionText(s *workspaceSession, text string, canRead bool) {
	state := &s.completion
	if state.uncertain {
		return
	}
	if !state.pending {
		if !canRead {
			return
		}
		prefix, _, ok := d.completionCurrent(s)
		if !ok {
			return
		}
		state = &s.completion
		state.pending, state.expectedHashes = true, completionHashes(prefix)
	}
	state.visible = false
	if state.waitingText > 0 {
		state.waitingText--
	}
	if text == "" || !utf8.ValidString(text) || strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 || len(state.expectedHashes)+utf8.RuneCountInString(text) > 513 {
		state.invalidate()
		return
	}
	for _, r := range text {
		state.expectedHashes = append(state.expectedHashes, completionHashAppend(state.expectedHashes[len(state.expectedHashes)-1], r))
	}
}
func (d *desktop) handleCompletionKey(s *workspaceSession, mods ui.Modifiers, key ui.Key) bool {
	if mods == 0 && s.completion.visible {
		prefix, _, ok := d.completionCurrent(s)
		if ok && !s.completion.pending && !s.completion.uncertain && prefix == s.completion.prefix && len(s.completion.items) > 0 {
			switch key {
			case ui.KeyEscape:
				s.completion.dismissed = prefix
				s.completion.visible = false
				return true
			case ui.KeyUp:
				s.completion.selected = (s.completion.selected + len(s.completion.items) - 1) % len(s.completion.items)
				return true
			case ui.KeyDown:
				s.completion.selected = (s.completion.selected + 1) % len(s.completion.items)
				return true
			case ui.KeyTab:
				if d.acceptCompletion(s, s.completion.selected) {
					return true
				}
			}
		}
	}
	d.noteCompletionKey(s, mods, key)
	return false
}
func (d *desktop) acceptCompletion(s *workspaceSession, index int) bool {
	prefix, _, ok := d.completionCurrent(s)
	if !ok || !s.completion.visible || s.completion.pending || s.completion.uncertain || prefix != s.completion.prefix || index < 0 || index >= len(s.completion.items) {
		return false
	}
	item := s.completion.items[index]
	var input string
	if item.replace {
		if strings.IndexFunc(prefix, func(r rune) bool { return unicode.Is(unicode.M, r) }) >= 0 {
			return false
		}
		input = strings.Repeat("\x7f", utf8.RuneCountInString(prefix)) + item.value
	} else {
		if !strings.HasPrefix(item.value, prefix) || len(item.value) <= len(prefix) {
			return false
		}
		input = item.value[len(prefix):]
	}
	s.completion.visible = false
	s.completion.dismissed = prefix
	s.completion.pending, s.completion.expectedHashes = true, completionHashes(item.value)
	s.term.Send([]byte(input))
	d.pendingTerminalFocus = s.id
	return true
}
func (d *desktop) terminalCompletionView(c *ui.Context, s *workspaceSession, view ui.Element) {
	prefix, t, ok := d.completionCurrent(s)
	if !ok || !view.Focused() {
		if s.completion.pending {
			s.completion.invalidate()
		}
		s.completion.visible = false
		s.completion.prefix = ""
		s.completion.dismissed = ""
		s.completion.items = nil
		return
	}
	hashes := completionHashes(prefix)
	matches := len(hashes) == len(s.completion.expectedHashes) && hashes[len(hashes)-1] == s.completion.expectedHashes[len(s.completion.expectedHashes)-1]
	if s.completion.uncertain || (s.completion.pending && (s.completion.waitingText > 0 || !matches)) {
		s.completion.visible = false
		s.completion.items = nil
		return
	}
	if s.completion.pending {
		s.completion.clearPending()
	}
	if prefix != s.completion.prefix {
		s.completion.prefix = prefix
		s.completion.selected = 0
		s.completion.dismissed = ""
	}
	items := completionCandidates(prefix, d.quickCommands)
	if len(items) == 0 || prefix == s.completion.dismissed {
		s.completion.visible = false
		s.completion.items = nil
		return
	}
	ox, oy, lineHeight, ok := t.NekoCaretOffset()
	bounds := view.Bounds()
	if !ok || bounds.W <= 0 || bounds.H <= 0 {
		s.completion.visible = false
		return
	}
	s.completion.items = items
	if s.completion.selected >= len(items) {
		s.completion.selected = 0
	}
	s.completion.visible = true
	windowW, windowH := c.Size()
	width := float32(245)
	height := float32(10 + len(items)*25)
	x := max(float32(0), min(bounds.X+ox, windowW-width))
	y := bounds.Y + oy
	if y+height > windowH {
		y = max(float32(0), bounds.Y+oy-lineHeight-height)
	}
	ui.Overlay(c, func() {
		ui.Column(c).Absolute().Left(x).Top(y).Width(width).Padding(5).Gap(1).Background(c.Theme().SurfaceHover).Border(1, c.Theme().Border).Radius(8).Children(func() {
			for i, item := range items {
				i, item := i, item
				label := item.value
				if item.label != "" {
					label = item.label + "  ·  " + item.value
				}
				b := ui.Button(c.Key(fmt.Sprintf("terminal-completion-%d-%d", s.id, i)), label).Label("补全："+label).FillWidth().Height(24).Padding(3, 7).BorderWidth(0).Background(c.Theme().SurfaceHover).Justify(ui.Start).FontSize(12).Tooltip("Tab 插入 · 上下选择 · Esc 关闭")
				if i == s.completion.selected {
					b = b.Background(c.Theme().SurfacePressed).Selected(true)
				}
				if b.Clicked() {
					d.acceptCompletion(s, i)
				}
			}
		})
	})
}
