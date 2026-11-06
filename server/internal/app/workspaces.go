package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxCloudWorkspaces = 100
const maxCloudWorkspaceBytes = 200 << 10

// Templates contain references, never SSH passwords, private keys, local paths
// or device trust decisions. The enclosing SQLite state is encrypted at rest.
type cloudWorkspaceEntry struct {
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	TabName     string `json:"tabName,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        string `json:"port,omitempty"`
	User        string `json:"user,omitempty"`
	TeamID      string `json:"teamId,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	FileDir     string `json:"fileDir,omitempty"`
	TerminalDir string `json:"terminalDir,omitempty"`
}
type cloudWorkspace struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Entries     []cloudWorkspaceEntry `json:"entries"`
	Active      int                   `json:"active"`
	SplitFirst  int                   `json:"splitFirst"`
	SplitSecond int                   `json:"splitSecond"`
	SplitLayout string                `json:"splitLayout,omitempty"`
	ViewMode    string                `json:"viewMode,omitempty"`
	RowPerc     []float32             `json:"rowPercent,omitempty"`
	ColumnPerc  []float32             `json:"columnPercent,omitempty"`
}
type cloudWorkspaceState struct {
	Items      []cloudWorkspace `json:"items"`
	Generation uint64           `json:"generation"`
}
type cloudWorkspaceSnapshot struct {
	Items    []cloudWorkspace `json:"items"`
	Revision string           `json:"revision"`
}
type cloudWorkspaceMutation struct {
	Items            []cloudWorkspace `json:"items"`
	ExpectedRevision string           `json:"expectedRevision"`
	SupportsTabNames bool             `json:"supportsTabNames,omitempty"`
}

var errWorkspaceConflict = errors.New("workspace state changed")
var errInvalidWorkspace = errors.New("invalid workspace state")
var errWorkspaceClientUpgrade = errors.New("workspace client upgrade required")

func workspaceText(s string, maxLen int) bool {
	return utf8.ValidString(s) && len(s) <= maxLen && strings.IndexFunc(s, unicode.IsControl) < 0
}
func validateCloudWorkspaces(items []cloudWorkspace) error {
	if len(items) > maxCloudWorkspaces {
		return errInvalidWorkspace
	}
	ids := map[string]bool{}
	for i, w := range items {
		id, err := hex.DecodeString(w.ID)
		if err != nil || len(id) != 16 || hex.EncodeToString(id) != w.ID || ids[w.ID] || w.Name == "" || w.Name != strings.TrimSpace(w.Name) || !workspaceText(w.Name, 120) || len(w.Entries) == 0 || len(w.Entries) > 16 || w.Active < 0 || w.Active >= len(w.Entries) {
			return errInvalidWorkspace
		}
		ids[w.ID] = true
		for _, previous := range items[:i] {
			if strings.EqualFold(previous.Name, w.Name) {
				return errInvalidWorkspace
			}
		}
		if w.SplitLayout != "" && w.SplitLayout != "side" && w.SplitLayout != "stacked" {
			return errInvalidWorkspace
		}
		if w.ViewMode != "" && w.ViewMode != "focus" && w.ViewMode != "grid" {
			return errInvalidWorkspace
		}
		none := w.SplitFirst == -1 && w.SplitSecond == -1
		if w.ViewMode != "" && !none {
			return errInvalidWorkspace
		}
		if !none && (w.SplitFirst < 0 || w.SplitSecond < 0 || w.SplitFirst >= len(w.Entries) || w.SplitSecond >= len(w.Entries) || w.SplitFirst == w.SplitSecond || (w.Active != w.SplitFirst && w.Active != w.SplitSecond)) {
			return errInvalidWorkspace
		}
		for _, values := range [][]float32{w.RowPerc, w.ColumnPerc} {
			if len(values) != 0 && ((len(values) != 12 && len(values) != 16) || w.ViewMode == "") {
				return errInvalidWorkspace
			}
			for _, v := range values {
				if math.IsNaN(float64(v)) || v < 0 || v > 100 {
					return errInvalidWorkspace
				}
			}
		}
		for _, e := range w.Entries {
			if e.TabName != strings.TrimSpace(e.TabName) || !workspaceText(e.TabName, 120) {
				return errInvalidWorkspace
			}
			for _, text := range []string{e.Label, e.Host, e.Port, e.User, e.TeamID, e.AccountID, e.FileDir, e.TerminalDir} {
				if !workspaceText(text, 4096) {
					return errInvalidWorkspace
				}
			}
			switch e.Kind {
			case "local":
				if e.Host != "" || e.Port != "" || e.User != "" || e.TeamID != "" || e.AccountID != "" || e.FileDir != "" || e.TerminalDir != "" {
					return errInvalidWorkspace
				}
			case "cloud":
				if len(e.Host) == 0 || len(e.Host) > 253 || strings.ContainsAny(e.Host, "/\\@ \t\r\n") || len(e.User) == 0 || len(e.User) > 128 || strings.ContainsAny(e.User, "/\\@ \t\r\n") || len(e.AccountID) == 0 || len(e.AccountID) > 128 || len(e.TeamID) > 128 || strings.ContainsAny(e.AccountID+e.TeamID, "/\\") {
					return errInvalidWorkspace
				}
				port := 0
				for _, c := range e.Port {
					if c < '0' || c > '9' {
						return errInvalidWorkspace
					}
					port = port*10 + int(c-'0')
					if port > 65535 {
						return errInvalidWorkspace
					}
				}
				if port < 1 || len(e.Port) > 5 || strconv.Itoa(port) != e.Port || (e.TerminalDir != "" && (!path.IsAbs(e.TerminalDir) || path.Clean(e.TerminalDir) != e.TerminalDir)) {
					return errInvalidWorkspace
				}
			default:
				return errInvalidWorkspace
			}
		}
	}
	data, err := json.Marshal(items)
	if err != nil || len(data) > maxCloudWorkspaceBytes {
		return errInvalidWorkspace
	}
	return nil
}

func validateCloudWorkspaceDocument(doc storeDocument) error {
	if len(doc.Workspaces) > len(doc.Users) {
		return errInvalidWorkspace
	}
	for user, state := range doc.Workspaces {
		if doc.Version < 5 && hasWorkspaceTabNames(state.Items) {
			return errInvalidWorkspace
		}
		if _, ok := doc.Users[user]; !ok || doc.Version < 3 || validateCloudWorkspaces(state.Items) != nil {
			return errInvalidWorkspace
		}
	}
	return nil
}

func hasWorkspaceTabNames(items []cloudWorkspace) bool {
	for _, w := range items {
		for _, entry := range w.Entries {
			if entry.TabName != "" {
				return true
			}
		}
	}
	return false
}

func cloneCloudWorkspaces(items []cloudWorkspace) []cloudWorkspace {
	result := make([]cloudWorkspace, len(items))
	for i, item := range items {
		result[i] = item
		result[i].Entries = append([]cloudWorkspaceEntry(nil), item.Entries...)
		result[i].RowPerc = append([]float32(nil), item.RowPerc...)
		result[i].ColumnPerc = append([]float32(nil), item.ColumnPerc...)
	}
	return result
}
func workspaceView(state cloudWorkspaceState) cloudWorkspaceSnapshot {
	items := cloneCloudWorkspaces(state.Items)
	data, _ := json.Marshal(cloudWorkspaceState{Items: items, Generation: state.Generation})
	revision := sha256.Sum256(data)
	return cloudWorkspaceSnapshot{Items: items, Revision: hex.EncodeToString(revision[:])}
}
func (s *Store) listCloudWorkspaces(user string) (cloudWorkspaceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user = normalizeUsername(user)
	if !s.activeUserLocked(user) {
		return cloudWorkspaceSnapshot{}, errTeamUnavailable
	}
	return workspaceView(s.doc.Workspaces[user]), nil
}
func (s *Store) replaceCloudWorkspaces(user string, input cloudWorkspaceMutation) (cloudWorkspaceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user = normalizeUsername(user)
	if !s.activeUserLocked(user) {
		return cloudWorkspaceSnapshot{}, errTeamUnavailable
	}
	state := s.doc.Workspaces[user]
	if input.ExpectedRevision != workspaceView(state).Revision {
		return cloudWorkspaceSnapshot{}, errWorkspaceConflict
	}
	if !input.SupportsTabNames && (hasWorkspaceTabNames(state.Items) || hasWorkspaceTabNames(input.Items)) {
		return cloudWorkspaceSnapshot{}, errWorkspaceClientUpgrade
	}
	if state.Generation == ^uint64(0) || validateCloudWorkspaces(input.Items) != nil {
		return cloudWorkspaceSnapshot{}, errInvalidWorkspace
	}
	next := cloudWorkspaceState{Items: cloneCloudWorkspaces(input.Items), Generation: state.Generation + 1}
	err := s.transactionalLocked(func() error {
		if s.doc.Workspaces == nil {
			s.doc.Workspaces = make(map[string]cloudWorkspaceState)
		}
		s.doc.Workspaces[user] = next
		s.doc.Version = max(s.doc.Version, 3)
		if hasWorkspaceTabNames(next.Items) {
			s.doc.Version = max(s.doc.Version, 5)
		}
		return nil
	})
	if err != nil {
		return cloudWorkspaceSnapshot{}, err
	}
	return workspaceView(next), nil
}
func (s *Server) workspaceResource(w http.ResponseWriter, r *http.Request, user string) {
	switch r.Method {
	case http.MethodGet:
		state, err := s.store.listCloudWorkspaces(user)
		if err != nil {
			s.error(w, http.StatusForbidden, "forbidden", "同步账号不可用")
			return
		}
		s.json(w, http.StatusOK, state)
	case http.MethodPut:
		var input cloudWorkspaceMutation
		if !s.decode(w, r, &input) {
			return
		}
		state, err := s.store.replaceCloudWorkspaces(user, input)
		switch {
		case errors.Is(err, errWorkspaceConflict):
			s.error(w, http.StatusConflict, "workspace_conflict", "云工作区已变化，请刷新后重试")
		case errors.Is(err, errWorkspaceClientUpgrade):
			s.error(w, http.StatusConflict, "workspace_client_upgrade", "请升级客户端以保护工作区终端名称")
		case errors.Is(err, errInvalidWorkspace):
			s.error(w, http.StatusBadRequest, "invalid_workspace", "云工作区内容无效")
		case errors.Is(err, errTeamUnavailable):
			s.error(w, http.StatusForbidden, "forbidden", "同步账号不可用")
		case err != nil:
			s.error(w, http.StatusInternalServerError, "storage_failed", "无法保存云工作区")
		default:
			s.json(w, http.StatusOK, state)
		}
	default:
		s.error(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法无效")
	}
}
