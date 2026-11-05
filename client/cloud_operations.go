package main

import "errors"

// mutateAndRefresh keeps the server mutation result separate from a later
// read: a failed refresh must never be reported as a failed write.
func mutateAndRefresh[T any](mutate func() error, refresh func() (T, error)) (T, error, error) {
	var zero T
	if err := mutate(); err != nil {
		return zero, err, nil
	}
	value, err := refresh()
	return value, nil, err
}

type cloudInitialState struct {
	accounts                       []cloudAccount
	teams                          []cloudTeam
	members                        []cloudTeamMember
	accountErr, teamErr, memberErr error
	groups                         cloudGroupSnapshot
	groupErr                       error
}

type cloudScopeState struct {
	accounts              []cloudAccount
	members               []cloudTeamMember
	accountErr, memberErr error
	groups                cloudGroupSnapshot
	groupErr              error
}

// The connection list determines whether the scope can be selected. Members
// are a separate read and a temporary failure must not hide usable accounts.
func loadCloudScope(client *cloudClient, id string) cloudScopeState {
	next := *client
	next.teamID = id
	state := cloudScopeState{}
	state.accounts, state.accountErr = next.list()
	if state.accountErr == nil && next.cloudGroups {
		state.groups, state.groupErr = next.listCloudGroups()
	}
	if state.accountErr == nil && id != "" {
		state.members, state.memberErr = next.members()
	}
	return state
}

// A successful login remains usable even if either independent read fails.
func loadCloudInitialState(c *cloudClient, selectSoleTeam bool) cloudInitialState {
	var state cloudInitialState
	c.discoverCapabilities()
	state.teams, state.teamErr = c.teams()
	if selectSoleTeam && state.teamErr == nil && len(state.teams) == 1 {
		c.teamID = state.teams[0].ID
		state.members, state.memberErr = c.members()
	}
	state.accounts, state.accountErr = c.list()
	if state.accountErr == nil && c.cloudGroups {
		state.groups, state.groupErr = c.listCloudGroups()
	}
	return state
}

func saveCloudAndRefresh(c *cloudClient, account cloudAccount) (string, []cloudAccount, error, error) {
	saved, accounts, mutationErr, refreshErr := saveCloudAndRefreshVersioned(c, account)
	return saved.ID, accounts, mutationErr, refreshErr
}

func saveCloudAndRefreshVersioned(c *cloudClient, account cloudAccount) (cloudAccount, []cloudAccount, error, error) {
	saved, mutationErr := c.saveReturningRecord(account)
	if mutationErr != nil {
		return cloudAccount{}, nil, mutationErr, nil
	}
	accounts, refreshErr := c.list()
	if refreshErr == nil {
		found := false
		for _, account := range accounts {
			if account.ID == saved.ID {
				found = true
				break
			}
		}
		if !found {
			refreshErr = errors.New("服务确认的连接未出现在刷新结果中")
		}
	}
	return saved, accounts, nil, refreshErr
}
