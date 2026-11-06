// mygooverlay applies a narrow, verified input-event hook to the pinned MyGo
// terminal at build time in a disposable build directory. It does not modify
// the module cache or commit a dependency copy to the repository.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type module struct{ Dir, Version string }

func replaceOnce(s, before, after string) (string, error) {
	if strings.Count(s, before) != 1 {
		return "", errors.New("unexpected MyGo source structure")
	}
	return strings.Replace(s, before, after, 1), nil
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: mygooverlay command [args...]")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	download := exec.Command("go", "-C", filepath.Join(root, "client"), "mod", "download", "github.com/egoist/mygo")
	if out, err := download.CombinedOutput(); err != nil {
		return fmt.Errorf("download MyGo: %w: %s", err, out)
	}
	cmd := exec.Command("go", "-C", filepath.Join(root, "client"), "list", "-m", "-json", "github.com/egoist/mygo")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("locate MyGo: %w", err)
	}
	var mod module
	if err = json.Unmarshal(output, &mod); err != nil || mod.Version != "v0.7.4" || mod.Dir == "" {
		return errors.New("unsupported MyGo module version")
	}
	buildRoot := filepath.Join(root, "build", "mygo-overlay")
	if err = os.MkdirAll(buildRoot, 0700); err != nil {
		return err
	}
	buildRoot, err = filepath.EvalSymlinks(buildRoot)
	if err != nil {
		return err
	}
	within, err := filepath.Rel(root, buildRoot)
	if err != nil || within == "." || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		return errors.New("build directory is outside the workspace")
	}
	tmp, err := os.MkdirTemp(buildRoot, "run-")
	if err != nil {
		return err
	}
	tmp, err = filepath.EvalSymlinks(tmp)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(buildRoot, tmp)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("temporary build path is outside build directory")
	}
	defer os.RemoveAll(tmp)
	mygoCopy := filepath.Join(tmp, "mygo")
	if err = copyModule(mod.Dir, mygoCopy); err != nil {
		return err
	}
	files := map[string]string{"terminal.go": "0e6c5120625c114dffcfae38aa845aa9130d649f1611fad0f269f6f224961c5d", "view.go": "1d28d7c6d8f0ae6edc27ef9358bcce9d2a2c54a13c200d7b8883f5e4d5a8f789"}
	for name, expected := range files {
		original := filepath.Join(mod.Dir, "plugins", "terminal", name)
		data, err := os.ReadFile(original)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("MyGo %s checksum mismatch", name)
		}
		source := string(data)
		if name == "terminal.go" {
			source, err = patchTerminal(source)
		} else {
			source, err = patchView(source)
		}
		if err != nil {
			return fmt.Errorf("patch %s: %w", name, err)
		}
		patched := filepath.Join(mygoCopy, "plugins", "terminal", name)
		if err = os.WriteFile(patched, []byte(source), 0600); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "client", "go.mod"))
	if err != nil {
		return err
	}
	path := filepath.Join(tmp, "client.mod")
	data = append(data, []byte("\nreplace github.com/egoist/mygo => "+strconv.Quote(filepath.ToSlash(mygoCopy))+"\n")...)
	if err = os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	sums, err := os.ReadFile(filepath.Join(root, "client", "go.sum"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(tmp, "client.sum"), sums, 0600); err != nil {
		return err
	}
	flags := os.Getenv("GOFLAGS")
	if strings.Contains(flags, "-modfile") {
		return errors.New("GOFLAGS already specifies a modfile")
	}
	cmd = exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "GOFLAGS="+flags+` "-modfile=`+path+`"`, "GOWORK=off", "NEKOSSH_REQUIRE_BROADCAST=1")
	if err = cmd.Run(); err != nil {
		return err
	}
	return nil
}

func copyModule(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("MyGo module contains a symbolic link")
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "MyGo input overlay:", err)
		os.Exit(1)
	}
}

func patchTerminal(s string) (string, error) {
	var err error
	s, err = replaceOnce(s, "type Terminal struct {\n\topts Options", `type Terminal struct {
	nekoKey func(int, int, int, int, int, string, bool)
	nekoText func(string)
	nekoPaste func(transfer.Data)
	nekoEncoder *vt.KeyEncoder
	nekoPromptSerial uint64
	nekoCompletionInstalled bool
	nekoOriginalKey func(ui.Modifiers, ui.Key) bool
	nekoPromptX, nekoPromptY, nekoPromptCols, nekoPromptRows int
	opts Options`)
	if err != nil {
		return "", err
	}
	s, err = replaceOnce(s, "\t\tt.term.Write(p)\n\t\tev, t.events = t.events, events{}", `		t.term.Write(p)
		if source, ok := t.src.(interface { NekoPromptState() (uint64, bool) }); ok {
			serial, active := source.NekoPromptState()
			if active && serial != 0 && serial != t.nekoPromptSerial {
				t.nekoPromptSerial = serial
				t.nekoPromptX, t.nekoPromptY = t.term.Cursor()
				t.nekoPromptCols, t.nekoPromptRows = t.term.Size()
			}
		}
		ev, t.events = t.events, events{}`)
	if err != nil {
		return "", err
	}
	s, err = replaceOnce(s, "// Send sends data to the program as if typed.", terminalHooks+"\n// Send sends data to the program as if typed.")
	if err != nil {
		return "", err
	}
	return replaceOnce(s, "\tt.endSearch()\n\tt.term.Free()", "\tt.endSearch()\n\tif t.nekoEncoder != nil { t.nekoEncoder.Free(); t.nekoEncoder = nil }\n\tt.nekoKey, t.nekoText, t.nekoPaste = nil, nil, nil\n\tt.term.Free()")
}

const terminalHooks = `// NekoSetCompletionKey installs a UI-thread key handler before terminal bindings.
func (t *Terminal) NekoSetCompletionKey(handler func(ui.Modifiers, ui.Key) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed { return }
	if !t.nekoCompletionInstalled { t.nekoOriginalKey, t.nekoCompletionInstalled = t.opts.OnKey, true }
	original := t.nekoOriginalKey
	t.opts.OnKey = func(mods ui.Modifiers, key ui.Key) bool {
		if handler != nil && handler(mods, key) { return true }
		return original != nil && original(mods, key)
	}
}

// NekoPromptLine reads only a shell-reported single-line prompt at the cursor.
func (t *Terminal) NekoPromptLine() (string, int, int, int, int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.term == nil || t.nekoPromptSerial == 0 || t.term.AltScreen() || !t.term.AtBottom() { return "", 0, 0, 0, 0, false }
	source, ok := t.src.(interface { NekoPromptState() (uint64, bool) })
	if !ok { return "", 0, 0, 0, 0, false }
	serial, active := source.NekoPromptState()
	cols, rows := t.term.Size()
	x, y := t.term.Cursor()
	if !active || serial != t.nekoPromptSerial || cols != t.nekoPromptCols || rows != t.nekoPromptRows || y != t.nekoPromptY || x < t.nekoPromptX || x >= cols-1 { return "", 0, 0, 0, 0, false }
	text, positions := t.term.RowText(y)
	var out strings.Builder
	for i, r := range text {
		if positions[i] >= t.nekoPromptX && positions[i] < x { out.WriteRune(r) }
		if positions[i] >= x && r != ' ' { return "", 0, 0, 0, 0, false }
	}
	return out.String(), x, y, cols, rows, true
}

// NekoCaretOffset gives the current cursor position in DIPs inside its view.
func (t *Terminal) NekoCaretOffset() (float32, float32, float32, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := t.v
	if t.term == nil || v == nil || v.scale <= 0 || v.cellW <= 0 || v.cellH <= 0 { return 0, 0, 0, false }
	x, y := t.term.Cursor()
	return float32(v.ox + x*v.cellW)/v.scale, float32(v.oy + (y+1)*v.cellH)/v.scale, float32(v.cellH)/v.scale, true
}

// NekoSetInputHooks registers user input hooks. Program replies are excluded.
func (t *Terminal) NekoSetInputHooks(key func(int, int, int, int, int, string, bool), text func(string), paste func(transfer.Data)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed { t.nekoKey, t.nekoText, t.nekoPaste = key, text, paste }
}

func (t *Terminal) NekoInputAvailable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed && t.term != nil
}

// NekoReplayKey encodes for this terminal's modes without invoking hooks.
func (t *Terminal) NekoReplayKey(action, key, mods, consumed, unshifted int, text string, composing bool) bool {
	t.mu.Lock()
	defer func() { t.mu.Unlock(); t.redraw() }()
	if t.term == nil || t.closed { return false }
	if t.nekoEncoder == nil {
		var err error
		t.nekoEncoder, err = vt.NewKeyEncoder()
		if err != nil { return false }
	}
	t.nekoEncoder.Sync(t.term, t.opts.OptionAsAlt)
	b := t.nekoEncoder.Encode(vt.KeyEvent{Action: vt.KeyAction(action), Key: vt.Key(key), Mods: vt.Mods(mods), Consumed: vt.Mods(consumed), Unshifted: rune(unshifted), Text: text, Composing: composing})
	if len(b) > 0 {
		t.in.push(append([]byte(nil), b...))
		if action != int(vt.KeyRelease) { t.term.ScrollToBottom(); t.term.SetSelection(nil) }
	}
	return true
}

func (t *Terminal) NekoReplayText(text string) bool {
	t.mu.Lock()
	if t.term == nil || t.closed { t.mu.Unlock(); return false }
	t.term.ScrollToBottom()
	t.term.SetSelection(nil)
	if text != "" { t.in.push([]byte(text)) }
	t.mu.Unlock()
	t.redraw()
	return true
}`

func patchView(s string) (string, error) {
	var err error
	s, err = replaceOnce(s, "\tb := v.keys.Encode(k)\n\tif len(b) == 0 {", `	b := v.keys.Encode(k)
	if t.nekoKey != nil { t.nekoKey(int(k.Action), int(k.Key), int(k.Mods), int(k.Consumed), int(k.Unshifted), k.Text, k.Composing) }
	if len(b) == 0 {`)
	if err != nil {
		return "", err
	}
	s, err = replaceOnce(s, "\tt.mu.Unlock()\n\tt.Send([]byte(text))\n\tv.blinkStart = time.Now()", `	onText := t.nekoText
	t.mu.Unlock()
	if onText != nil { onText(text) }
	t.Send([]byte(text))
	v.blinkStart = time.Now()`)
	if err != nil {
		return "", err
	}
	return replaceOnce(s, "\tt.pasteData(d, held)\n\treturn true", `	t.mu.Lock()
	onPaste := t.nekoPaste
	t.mu.Unlock()
	if onPaste != nil { onPaste(d.Snapshot()) }
	t.pasteData(d, held)
	return true`)
}
