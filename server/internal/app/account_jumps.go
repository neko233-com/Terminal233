package app

import (
	"encoding/hex"
	"errors"
	"net/http"
)

const maxCloudJumpHosts = 8

var (
	errInvalidAccountJump = errors.New("invalid account jump chain")
	errAccountJumpInUse   = errors.New("account is used as a jump host")
	errJumpClientUpgrade  = errors.New("client does not support account jump chains")
)

func jumpClientSupported(r *http.Request) bool { return r.Header.Get("X-Neko-Jump-Support") == "1" }

func validCloudJumpID(id string) bool {
	if id == "" {
		return true
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 24 && hex.EncodeToString(decoded) == id
}

func jumpIDPointer(id string) *string {
	if id == "" {
		return nil
	}
	value := id
	return &value
}

func accountJumpsPresent(accounts []Account) bool {
	for _, account := range accounts {
		if account.JumpID != nil && *account.JumpID != "" {
			return true
		}
	}
	return false
}

// Every reference remains in one personal/team scope. Reject missing hosts,
// loops and chains longer than eight hops before committing the transaction.
func validateAccountJumps(items []storedAccount) error {
	byID := make(map[string]storedAccount, len(items))
	hasJumps := false
	for _, item := range items {
		hasJumps = hasJumps || item.JumpID != ""
	}
	for _, item := range items {
		if hasJumps && (item.ID == "" || !validCloudJumpID(item.ID)) {
			return errInvalidAccountJump
		}
		if _, exists := byID[item.ID]; exists && hasJumps {
			return errInvalidAccountJump
		}
		byID[item.ID] = item
	}
	for _, item := range items {
		seen := map[string]bool{item.ID: true}
		id := item.JumpID
		for depth := 0; id != ""; depth++ {
			if depth >= maxCloudJumpHosts || !validCloudJumpID(id) || seen[id] {
				return errInvalidAccountJump
			}
			next, ok := byID[id]
			if !ok {
				return errInvalidAccountJump
			}
			seen[id] = true
			id = next.JumpID
		}
	}
	return nil
}

func validateAccountJumpsDocument(doc storeDocument) error {
	check := func(items []storedAccount) error {
		if doc.Version < 6 {
			for _, item := range items {
				if item.JumpID != "" {
					return errInvalidAccountJump
				}
			}
		}
		return validateAccountJumps(items)
	}
	for _, items := range doc.Accounts {
		if err := check(items); err != nil {
			return err
		}
	}
	for _, team := range doc.Teams {
		if err := check(team.Accounts); err != nil {
			return err
		}
	}
	return nil
}

func accountJumpReferenced(items []storedAccount, id string) bool {
	for _, item := range items {
		if item.JumpID == id {
			return true
		}
	}
	return false
}
