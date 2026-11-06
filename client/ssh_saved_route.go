package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// A route is built from the latest in-memory profile revision. It is never
// serialized: the referenced hosts retain their own protected credentials.
type savedSSHRoute struct {
	hops      []connectionSettings
	signature [32]byte
	jumpID    string
}

func (d *desktop) validateSavedJumpChoice(jumpID string) error {
	var targetID string
	if p, ok := d.currentProfile(profileID(d.selectedProfile)); ok {
		targetID = p.StableID
	}
	seen := make(map[string]bool)
	for id, depth := jumpID, 0; id != ""; depth++ {
		if !validRandomID(id) || id == targetID || seen[id] || depth >= maxSSHJumpHosts {
			return errors.New("跳板链存在循环或超过 8 跳")
		}
		p, ok := d.forwardRuleHost(id)
		if !ok {
			return errors.New("跳板主机不存在，请重新选择")
		}
		seen[id] = true
		id = p.Settings.JumpID
	}
	return nil
}

func (d *desktop) resolveSavedRoute(target connectionSettings) (connectionSettings, error) {
	target.route = nil
	if target.JumpID == "" {
		return target, nil
	}
	if !validRandomID(target.JumpID) {
		return connectionSettings{}, errors.New("保存的跳板标识无效")
	}
	address, err := target.address()
	if err != nil {
		return connectionSettings{}, err
	}
	seenIDs := map[string]bool{}
	seenAddresses := map[string]bool{strings.ToLower(address): true}
	var reverse []connectionSettings
	var components []string
	for id := target.JumpID; id != ""; {
		if seenIDs[id] || len(reverse) >= maxSSHJumpHosts {
			return connectionSettings{}, errors.New("跳板链存在循环或超过 8 跳")
		}
		seenIDs[id] = true
		profile, ok := d.forwardRuleHost(id)
		if !ok {
			return connectionSettings{}, errors.New("保存的跳板主机不存在，请重新选择")
		}
		settings, err := d.profileCredential(profile)
		if err != nil {
			return connectionSettings{}, fmt.Errorf("跳板 %s：%w", profile.Label, err)
		}
		address, err := settings.address()
		if err != nil || seenAddresses[strings.ToLower(address)] {
			return connectionSettings{}, errors.New("跳板链包含无效或重复的服务器地址")
		}
		if !strings.HasPrefix(settings.Fingerprint, "SHA256:") {
			return connectionSettings{}, fmt.Errorf("请先核对跳板 %s 的主机指纹", profile.Label)
		}
		seenAddresses[strings.ToLower(address)] = true
		settings.route = nil
		reverse = append(reverse, settings)
		components = append(components, id, address, settings.User, settings.KeyPath, settings.Fingerprint, settings.JumpID)
		id = settings.JumpID
	}
	hops := make([]connectionSettings, len(reverse))
	for i := range reverse {
		hops[i] = reverse[len(reverse)-1-i]
	}
	target.route = &savedSSHRoute{hops: hops, signature: connectionDraftHash(components...), jumpID: target.JumpID}
	return target, nil
}

func (s connectionSettings) savedRouteSignature() string {
	if s.route == nil {
		return ""
	}
	return hex.EncodeToString(s.route.signature[:])
}

func (d *desktop) validateSessionSavedRoute(settings connectionSettings) error {
	if settings.JumpID == "" {
		return nil
	}
	if settings.route == nil || settings.route.jumpID != settings.JumpID {
		return errors.New("跳板路径不可用，请重新选择主机")
	}
	current, err := d.resolveSavedRoute(settings)
	if err != nil || current.route.signature != settings.route.signature {
		return errors.New("保存的跳板路径或指纹已变化，请重新建立连接")
	}
	return nil
}
