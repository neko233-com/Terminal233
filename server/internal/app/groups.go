package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxCloudGroups = 1000

type cloudGroupState struct {
	Groups      []string          `json:"groups"`
	Assignments map[string]string `json:"assignments"`
	Generation  uint64            `json:"generation"`
}

type cloudGroupSnapshot struct {
	Groups      []string          `json:"groups"`
	Assignments map[string]string `json:"assignments"`
	AccountIDs  []string          `json:"accountIds"`
	Revision    string            `json:"revision"`
}

type cloudGroupMutation struct {
	Groups           []string          `json:"groups"`
	Assignments      map[string]string `json:"assignments"`
	ExpectedRevision string            `json:"expectedRevision"`
}

var errGroupConflict = errors.New("group state changed")
var errInvalidGroups = errors.New("invalid group state")

func validCloudGroupPath(path string) bool {
	if path == "" || len(path) > 512 || !utf8.ValidString(path) {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part != strings.TrimSpace(part) || utf8.RuneCountInString(part) > 64 || strings.ContainsRune(part, '\\') {
			return false
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func validateCloudGroups(state cloudGroupState, accounts []storedAccount) error {
	if len(state.Groups) > maxCloudGroups || len(state.Assignments) > maxAccountsPerUser {
		return errInvalidGroups
	}
	size := 0
	groups := make(map[string]bool, len(state.Groups))
	for _, group := range state.Groups {
		size += len(group)
		if !validCloudGroupPath(group) || groups[group] {
			return errInvalidGroups
		}
		groups[group] = true
	}
	for group := range groups {
		if i := strings.LastIndex(group, "/"); i > 0 && !groups[group[:i]] {
			return errInvalidGroups
		}
	}
	ids := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		ids[a.ID] = true
	}
	for id, group := range state.Assignments {
		size += len(id) + len(group)
		if !ids[id] || !groups[group] {
			return errInvalidGroups
		}
	}
	if size > 128<<10 {
		return errInvalidGroups
	}
	return nil
}

func validateCloudGroupDocument(doc storeDocument) error {
	if len(doc.Groups) > len(doc.Users) {
		return errInvalidGroups
	}
	for user, state := range doc.Groups {
		if _, ok := doc.Users[user]; !ok {
			return errInvalidGroups
		}
		if err := validateCloudGroups(state, doc.Accounts[user]); err != nil {
			return err
		}
		if doc.Version < 2 && (len(state.Groups) > 0 || len(state.Assignments) > 0 || state.Generation > 0) {
			return errInvalidGroups
		}
	}
	for _, team := range doc.Teams {
		if team.Groups == nil {
			continue
		}
		if err := validateCloudGroups(*team.Groups, team.Accounts); err != nil {
			return err
		}
		if doc.Version < 2 && (len(team.Groups.Groups) > 0 || len(team.Groups.Assignments) > 0 || team.Groups.Generation > 0) {
			return errInvalidGroups
		}
	}
	return nil
}

func cloudGroupView(state cloudGroupState, accounts []storedAccount) cloudGroupSnapshot {
	groups := append([]string{}, state.Groups...)
	sort.Strings(groups)
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	assignments := make(map[string]string, len(state.Assignments))
	for id, group := range state.Assignments {
		assignments[id] = group
	}
	data, _ := json.Marshal(struct {
		Groups      []string          `json:"groups"`
		Assignments map[string]string `json:"assignments"`
		Accounts    []string          `json:"accounts"`
		Generation  uint64            `json:"generation"`
	}{groups, assignments, ids, state.Generation})
	revision := sha256.Sum256(data)
	return cloudGroupSnapshot{Groups: groups, Assignments: assignments, AccountIDs: ids, Revision: hex.EncodeToString(revision[:])}
}

func (s *Store) groupScopeLocked(user, team string, write bool) (cloudGroupState, []storedAccount, error) {
	user = normalizeUsername(user)
	if !s.activeUserLocked(user) {
		return cloudGroupState{}, nil, errTeamUnavailable
	}
	if team == "" {
		return s.doc.Groups[user], s.doc.Accounts[user], nil
	}
	t, ok := s.doc.Teams[team]
	role := t.Members[user]
	if !ok || t.Disabled || role == "" || (write && role != "owner" && role != "admin") {
		return cloudGroupState{}, nil, errTeamUnavailable
	}
	if t.Groups == nil {
		return cloudGroupState{}, t.Accounts, nil
	}
	return *t.Groups, t.Accounts, nil
}

func (s *Store) listCloudGroups(user, team string) (cloudGroupSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, accounts, err := s.groupScopeLocked(user, team, false)
	if err != nil {
		return cloudGroupSnapshot{}, err
	}
	return cloudGroupView(state, accounts), nil
}

func (s *Store) replaceCloudGroups(user, team string, input cloudGroupMutation) (cloudGroupSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, accounts, err := s.groupScopeLocked(user, team, true)
	if err != nil {
		return cloudGroupSnapshot{}, err
	}
	current := cloudGroupView(state, accounts)
	if input.ExpectedRevision != current.Revision {
		return cloudGroupSnapshot{}, errGroupConflict
	}
	if state.Generation == ^uint64(0) {
		return cloudGroupSnapshot{}, errInvalidGroups
	}
	next := cloudGroupState{Groups: append([]string{}, input.Groups...), Assignments: make(map[string]string, len(input.Assignments)), Generation: state.Generation + 1}
	for id, group := range input.Assignments {
		next.Assignments[id] = group
	}
	if err = validateCloudGroups(next, accounts); err != nil {
		return cloudGroupSnapshot{}, err
	}
	sort.Strings(next.Groups)
	err = s.transactionalLocked(func() error {
		if team == "" {
			if s.doc.Groups == nil {
				s.doc.Groups = make(map[string]cloudGroupState)
			}
			s.doc.Groups[normalizeUsername(user)] = next
		} else {
			t := s.doc.Teams[team]
			t.Groups = &next
			s.doc.Teams[team] = t
		}
		s.doc.Version = 2
		return nil
	})
	if err != nil {
		return cloudGroupSnapshot{}, err
	}
	return cloudGroupView(next, accounts), nil
}

func (s *Server) groupResource(w http.ResponseWriter, r *http.Request, user, team string) {
	switch r.Method {
	case http.MethodGet:
		state, err := s.store.listCloudGroups(user, team)
		if err != nil {
			s.error(w, http.StatusNotFound, "not_found", "同步空间不存在或无权访问")
			return
		}
		s.json(w, http.StatusOK, state)
	case http.MethodPut:
		var input cloudGroupMutation
		if !s.decode(w, r, &input) {
			return
		}
		state, err := s.store.replaceCloudGroups(user, team, input)
		switch {
		case errors.Is(err, errGroupConflict):
			s.error(w, http.StatusConflict, "group_conflict", "分组或连接列表已变化，请刷新重试")
		case errors.Is(err, errInvalidGroups):
			s.error(w, http.StatusBadRequest, "invalid_groups", "分组或连接归属无效")
		case errors.Is(err, errTeamUnavailable):
			s.error(w, http.StatusForbidden, "forbidden", "同步空间不存在或不可写")
		case err != nil:
			s.error(w, http.StatusInternalServerError, "storage_failed", "无法保存分组")
		default:
			s.json(w, http.StatusOK, state)
		}
	default:
		s.error(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法无效")
	}
}
