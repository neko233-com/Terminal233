package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Opt-in rendered review, with synthetic profiles and no network or credentials.
func TestWorkspaceVisualReview(t *testing.T) {
	root := os.Getenv("NEKOSSH_VISUAL_REVIEW")
	if root == "" {
		t.Skip("set NEKOSSH_VISUAL_REVIEW to capture rendered workspaces")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		name                    string
		width, height           int
		light, inspector, empty bool
	}{
		{"hosts-dark", 1200, 760, false, false, false}, {"hosts-light", 1200, 760, true, false, false},
		{"hosts-wide", 1600, 900, false, false, false}, {"hosts-compact", 640, 760, false, false, false},
		{"host-inspector", 1400, 900, false, true, false},
		{"hosts-empty-dark", 1200, 760, false, false, true}, {"hosts-empty-light", 1200, 760, true, false, true}, {"hosts-empty-compact", 640, 480, false, false, true},
	} {
		t.Run(state.name, func(t *testing.T) {
			d := &desktop{workspace: "connections", lightTheme: state.light, railExpanded: true}
			for i, p := range []struct{ name, host, group string }{
				{"Production API", "api.example.invalid", "Production"},
				{"Database", "db.example.invalid", "Production"},
				{"Staging", "staging.example.invalid", "Development"},
				{"Build agent", "build.example.invalid", "Development"},
				{"Archive", "archive.example.invalid", ""},
			} {
				d.profiles = append(d.profiles, profile{ID: profileID(string(rune('1' + i))), Label: p.name, Group: p.group, Settings: connectionSettings{Host: p.host, Port: "22", User: "devops"}})
			}
			d.profileGroups = []string{"Production", "Development"}
			if state.empty {
				d.profiles = nil
				d.profileGroups = nil
			}
			if state.inspector {
				d.selectedProfile = "1"
				d.loadProfile(d.profiles[0])
				d.editConnection = true
				d.captureConnectionDraft()
			}
			view := ui.NewTester(d.view, state.width, state.height)
			if _, ok := view.Find("+ 新建连接"); !ok {
				t.Fatal("host toolbar unavailable")
			}
			f, err := os.Create(filepath.Join(root, state.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, view.Image())
			closeErr := f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
	t.Run("terminal", func(t *testing.T) {
		term := groupTestTerminals(t, 1)[0]
		term.Feed([]byte("\x1b[38;5;40mdevops@production\x1b[0m:~/services/api$ git status\r\nOn branch main\r\nYour branch is up to date with 'origin/main'.\r\n\r\n\x1b[32mworking tree clean\x1b[0m\r\n\r\n\x1b[38;5;40mdevops@production\x1b[0m:~/services/api$ "))
		d := &desktop{workspace: "terminal", currentSession: 1, term: term, sessions: []*workspaceSession{{id: 1, label: "Production API", term: term}}}
		view := ui.NewTester(d.view, 1200, 760)
		if _, ok := view.Find("Terminal"); !ok {
			t.Fatal("terminal unavailable")
		}
		f, err := os.Create(filepath.Join(root, "terminal.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, view.Image())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	})
}
