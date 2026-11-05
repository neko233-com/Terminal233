package main

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
}

// A successful login remains usable even if either independent read fails.
func loadCloudInitialState(c *cloudClient, selectSoleTeam bool) cloudInitialState {
	var state cloudInitialState
	state.teams, state.teamErr = c.teams()
	if selectSoleTeam && state.teamErr == nil && len(state.teams) == 1 {
		c.teamID = state.teams[0].ID
		state.members, state.memberErr = c.members()
	}
	state.accounts, state.accountErr = c.list()
	return state
}
