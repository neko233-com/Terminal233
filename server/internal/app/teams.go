package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const maxTeamMembers = 1000
const maxTeamsPerUser = 50
const maxActiveTeamInvitations = 64

type teamRecord struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Members   map[string]string `json:"members"`
	Accounts  []storedAccount   `json:"accounts"`
	CreatedAt string            `json:"createdAt"`
	Disabled  bool              `json:"disabled,omitempty"`
	Groups    *cloudGroupState  `json:"groups,omitempty"`
}
type teamInvitation struct {
	TeamID       string `json:"teamId"`
	PasswordHash string `json:"passwordHash"`
	ExpiresAt    int64  `json:"expiresAt"`
	Remaining    int    `json:"remaining"`
}
type teamSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	MemberCount int    `json:"memberCount"`
	Disabled    bool   `json:"disabled"`
}
type teamMember struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

var (
	errTeamUnavailable   = errors.New("team unavailable")
	errTeamLimit         = errors.New("team limit reached")
	errInvalidInvitation = errors.New("invalid invitation")
	errOwnerRequired     = errors.New("owner cannot be removed")
)

func (s *Store) ensureTeamsLocked() {
	if s.doc.Teams == nil {
		s.doc.Teams = map[string]teamRecord{}
	}
	if s.doc.Invitations == nil {
		s.doc.Invitations = map[string]teamInvitation{}
	}
}
func (s *Store) transactionalLocked(change func() error) error {
	before, err := json.Marshal(s.doc)
	if err != nil {
		return err
	}
	if err = change(); err != nil {
		return err
	}
	if err = s.saveLocked(); err != nil {
		var restored storeDocument
		if decodeErr := json.Unmarshal(before, &restored); decodeErr != nil {
			return errors.Join(err, decodeErr)
		}
		s.doc = restored
		return err
	}
	return nil
}
func (s *Store) teamCountLocked(user string) int {
	n := 0
	for _, t := range s.doc.Teams {
		if t.Members[user] != "" {
			n++
		}
	}
	return n
}
func (s *Store) createTeam(user, name string) (teamSummary, error) {
	user = normalizeUsername(user)
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 128 {
		return teamSummary{}, errors.New("invalid team name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTeamsLocked()
	if !s.activeUserLocked(user) {
		return teamSummary{}, errTeamUnavailable
	}
	if s.teamCountLocked(user) >= maxTeamsPerUser {
		return teamSummary{}, errTeamLimit
	}
	id := newID()
	rec := teamRecord{ID: id, Name: name, Members: map[string]string{user: "owner"}, Accounts: []storedAccount{}, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	err := s.transactionalLocked(func() error { s.doc.Teams[id] = rec; return nil })
	if err != nil {
		return teamSummary{}, err
	}
	return teamSummary{ID: id, Name: name, Role: "owner", MemberCount: 1}, nil
}
func (s *Store) listTeams(user string) []teamSummary {
	user = normalizeUsername(user)
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []teamSummary{}
	if !s.activeUserLocked(user) {
		return result
	}
	for _, t := range s.doc.Teams {
		if role := t.Members[user]; role != "" {
			result = append(result, teamSummary{ID: t.ID, Name: t.Name, Role: role, MemberCount: len(t.Members), Disabled: t.Disabled})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (s *Store) teamMembers(user, id string) ([]teamMember, error) {
	user = normalizeUsername(user)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	if !ok || t.Disabled || !s.activeUserLocked(user) || t.Members[user] == "" {
		return nil, errTeamUnavailable
	}
	result := make([]teamMember, 0, len(t.Members))
	for name, role := range t.Members {
		result = append(result, teamMember{Username: name, Role: role})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Username < result[j].Username })
	return result, nil
}
func invitationKey(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
func (s *Store) createInvitation(user, id, password string, ttl time.Duration, uses int) (string, error) {
	if len(password) < 12 || len(password) > 256 || ttl < time.Minute || ttl > 7*24*time.Hour || uses < 1 || uses > 100 {
		return "", errInvalidInvitation
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTeamsLocked()
	team, ok := s.doc.Teams[id]
	role := team.Members[normalizeUsername(user)]
	if !ok || team.Disabled || !s.activeUserLocked(user) || (role != "owner" && role != "admin") {
		return "", errTeamUnavailable
	}
	active := 0
	now := time.Now().Unix()
	for _, inv := range s.doc.Invitations {
		if inv.TeamID == id && inv.ExpiresAt > now && inv.Remaining > 0 {
			active++
		}
	}
	if active >= maxActiveTeamInvitations {
		return "", errTeamLimit
	}
	token := newID()
	entry := teamInvitation{TeamID: id, PasswordHash: passwordHash, ExpiresAt: time.Now().Add(ttl).Unix(), Remaining: uses}
	err = s.transactionalLocked(func() error {
		for key, inv := range s.doc.Invitations {
			if inv.ExpiresAt <= now || inv.Remaining <= 0 {
				delete(s.doc.Invitations, key)
			}
		}
		s.doc.Invitations[invitationKey(token)] = entry
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}
func (s *Store) joinTeam(user, token, password string) (teamSummary, error) {
	user = normalizeUsername(user)
	if len(token) != 48 || len(password) > 256 {
		return teamSummary{}, errInvalidInvitation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTeamsLocked()
	id := invitationKey(token)
	inv, ok := s.doc.Invitations[id]
	if !ok || inv.ExpiresAt <= time.Now().Unix() || inv.Remaining < 1 || !verifyPassword(inv.PasswordHash, password) {
		return teamSummary{}, errInvalidInvitation
	}
	t, ok := s.doc.Teams[inv.TeamID]
	if !ok || t.Disabled || !s.activeUserLocked(user) {
		return teamSummary{}, errInvalidInvitation
	}
	if role := t.Members[user]; role != "" {
		return teamSummary{ID: t.ID, Name: t.Name, Role: role, MemberCount: len(t.Members)}, nil
	}
	if len(t.Members) >= maxTeamMembers || s.teamCountLocked(user) >= maxTeamsPerUser {
		return teamSummary{}, errTeamLimit
	}
	if _, ok = s.doc.Users[user]; !ok {
		return teamSummary{}, errTeamUnavailable
	}
	err := s.transactionalLocked(func() error {
		t.Members[user] = "member"
		s.doc.Teams[t.ID] = t
		inv.Remaining--
		s.doc.Invitations[id] = inv
		return nil
	})
	if err != nil {
		return teamSummary{}, err
	}
	return teamSummary{ID: t.ID, Name: t.Name, Role: "member", MemberCount: len(t.Members)}, nil
}
func (s *Store) removeTeamMember(actor, id, target string) error {
	actor = normalizeUsername(actor)
	target = normalizeUsername(target)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	if !ok || t.Disabled || !s.activeUserLocked(actor) || t.Members[actor] != "owner" {
		return errTeamUnavailable
	}
	if t.Members[target] == "owner" {
		return errOwnerRequired
	}
	if t.Members[target] == "" {
		return errTeamUnavailable
	}
	return s.transactionalLocked(func() error { delete(t.Members, target); s.doc.Teams[id] = t; return nil })
}

func (s *Store) listTeamAccounts(user, id string) ([]Account, error) {
	user = normalizeUsername(user)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	if !ok || t.Disabled || !s.activeUserLocked(user) || t.Members[user] == "" {
		return nil, errTeamUnavailable
	}
	result := make([]Account, 0, len(t.Accounts))
	for _, a := range t.Accounts {
		password, err := decryptString(s.key, a.EncryptedPassword)
		if err != nil {
			return nil, err
		}
		result = append(result, Account{ID: a.ID, Label: a.Label, Host: a.Host, Port: a.Port, Username: a.Username, Password: password, HostKeyFingerprint: a.HostKeyFingerprint, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, Tags: append([]string(nil), a.Tags...), JumpID: jumpIDPointer(a.JumpID)})
	}
	return result, nil
}
func (s *Store) saveTeamAccount(user, id string, a Account) (Account, error) {
	return s.saveTeamAccountForClient(user, id, a, true)
}

func (s *Store) saveTeamAccountForClient(user, id string, a Account, supportsJumps bool) (Account, error) {
	user = normalizeUsername(user)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	role := t.Members[user]
	if !ok || t.Disabled || !s.activeUserLocked(user) || (role != "owner" && role != "admin") {
		return Account{}, errTeamUnavailable
	}
	if !validAccountTags(a.Tags) {
		return Account{}, errors.New("invalid account tags")
	}
	if a.JumpID != nil && (!supportsJumps || !validCloudJumpID(*a.JumpID)) {
		return Account{}, errInvalidAccountJump
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	index := -1
	if a.ID == "" {
		if len(t.Accounts) >= maxAccountsPerUser {
			return Account{}, errAccountLimit
		}
		a.ID = newID()
		a.CreatedAt = now
	} else {
		for i, existing := range t.Accounts {
			if existing.ID == a.ID {
				if !supportsJumps && existing.JumpID != "" {
					return Account{}, errJumpClientUpgrade
				}
				if a.ExpectedUpdatedAt != "" && a.ExpectedUpdatedAt != existing.UpdatedAt {
					return Account{}, errAccountConflict
				}
				now = nextAccountTimestamp(existing.UpdatedAt)
				index = i
				a.CreatedAt = existing.CreatedAt
				if a.Tags == nil {
					a.Tags = append([]string(nil), existing.Tags...)
				}
				if a.JumpID == nil {
					a.JumpID = jumpIDPointer(existing.JumpID)
				}
				break
			}
		}
		if index < 0 {
			return Account{}, errAccountNotFound
		}
	}
	a.UpdatedAt = now
	a.ExpectedUpdatedAt = ""
	encrypted, err := encryptString(s.key, a.Password)
	if err != nil {
		return Account{}, err
	}
	jumpID := ""
	if a.JumpID != nil {
		jumpID = *a.JumpID
	}
	a.JumpID = jumpIDPointer(jumpID)
	record := storedAccount{ID: a.ID, Label: a.Label, Host: a.Host, Port: a.Port, Username: a.Username, EncryptedPassword: encrypted, HostKeyFingerprint: a.HostKeyFingerprint, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, Tags: append([]string(nil), a.Tags...), JumpID: jumpID}
	items := append([]storedAccount(nil), t.Accounts...)
	if index < 0 {
		items = append(items, record)
	} else {
		items[index] = record
	}
	if err := validateAccountJumps(items); err != nil {
		return Account{}, err
	}
	err = s.transactionalLocked(func() error {
		if len(record.Tags) > 0 {
			s.doc.Version = max(s.doc.Version, 4)
		}
		if record.JumpID != "" {
			s.doc.Version = max(s.doc.Version, 6)
		}
		t.Accounts = items
		s.doc.Teams[id] = t
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return a, nil
}
func (s *Store) deleteTeamAccount(user, id, accountID string) error {
	return s.deleteTeamAccountIfVersion(user, id, accountID, "")
}

func (s *Store) deleteTeamAccountIfVersion(user, id, accountID, expected string) error {
	user = normalizeUsername(user)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	role := t.Members[user]
	if !ok || t.Disabled || !s.activeUserLocked(user) || (role != "owner" && role != "admin") {
		return errTeamUnavailable
	}
	index := -1
	for i, a := range t.Accounts {
		if a.ID == accountID {
			if expected != "" && expected != a.UpdatedAt {
				return errAccountConflict
			}
			index = i
			break
		}
	}
	if index < 0 {
		return errAccountNotFound
	}
	if accountJumpReferenced(t.Accounts, accountID) {
		return errAccountJumpInUse
	}
	return s.transactionalLocked(func() error {
		items := append([]storedAccount(nil), t.Accounts[:index]...)
		items = append(items, t.Accounts[index+1:]...)
		t.Accounts = items
		if t.Groups != nil && t.Groups.Assignments != nil {
			delete(t.Groups.Assignments, accountID)
		}
		s.doc.Teams[id] = t
		return nil
	})
}

func (s *Store) adminTeams() []teamSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]teamSummary, 0, len(s.doc.Teams))
	for _, t := range s.doc.Teams {
		result = append(result, teamSummary{ID: t.ID, Name: t.Name, MemberCount: len(t.Members), Disabled: t.Disabled})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (s *Store) setTeamDisabled(id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	if !ok {
		return errTeamUnavailable
	}
	return s.transactionalLocked(func() error { t.Disabled = disabled; s.doc.Teams[id] = t; return nil })
}
func (s *Store) registerWithInvitation(username, password, token, invitePassword string) error {
	username = normalizeUsername(username)
	if len(token) != 48 || len(invitePassword) > 256 {
		return errInvalidInvitation
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTeamsLocked()
	if _, exists := s.doc.Users[username]; exists {
		return errUserExists
	}
	id := invitationKey(token)
	invite, ok := s.doc.Invitations[id]
	if !ok || invite.ExpiresAt <= time.Now().Unix() || invite.Remaining < 1 || !verifyPassword(invite.PasswordHash, invitePassword) {
		return errInvalidInvitation
	}
	team, ok := s.doc.Teams[invite.TeamID]
	if !ok || team.Disabled || len(team.Members) >= maxTeamMembers {
		return errInvalidInvitation
	}
	return s.transactionalLocked(func() error {
		s.doc.Users[username] = userRecord{Username: username, PasswordHash: passwordHash, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		s.doc.Accounts[username] = []storedAccount{}
		team.Members[username] = "member"
		s.doc.Teams[team.ID] = team
		invite.Remaining--
		s.doc.Invitations[id] = invite
		return nil
	})
}

type invitationSummary struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expiresAt"`
	Remaining int    `json:"remaining"`
}

func (s *Store) listInvitations(user, id string) ([]invitationSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	role := t.Members[normalizeUsername(user)]
	if !ok || t.Disabled || !s.activeUserLocked(user) || (role != "owner" && role != "admin") {
		return nil, errTeamUnavailable
	}
	result := []invitationSummary{}
	now := time.Now().Unix()
	for hash, inv := range s.doc.Invitations {
		if inv.TeamID == id && inv.ExpiresAt > now && inv.Remaining > 0 {
			result = append(result, invitationSummary{ID: hash, ExpiresAt: inv.ExpiresAt, Remaining: inv.Remaining})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExpiresAt < result[j].ExpiresAt })
	return result, nil
}
func (s *Store) revokeInvitation(user, id, inviteID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	role := t.Members[normalizeUsername(user)]
	inv, exists := s.doc.Invitations[inviteID]
	if !ok || t.Disabled || !s.activeUserLocked(user) || (role != "owner" && role != "admin") || !exists || inv.TeamID != id {
		return errTeamUnavailable
	}
	return s.transactionalLocked(func() error { delete(s.doc.Invitations, inviteID); return nil })
}
func (s *Store) setMemberRole(actor, id, target, role string) error {
	actor = normalizeUsername(actor)
	target = normalizeUsername(target)
	if role != "member" && role != "admin" {
		return errTeamUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.doc.Teams[id]
	if !ok || t.Disabled || !s.activeUserLocked(actor) || t.Members[actor] != "owner" || t.Members[target] == "" || t.Members[target] == "owner" {
		return errTeamUnavailable
	}
	return s.transactionalLocked(func() error { t.Members[target] = role; s.doc.Teams[id] = t; return nil })
}
