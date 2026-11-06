package main

import (
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

// A host key is associated with a network endpoint, not a username. Cloud
// metadata is not a local trust decision; only verified sessions and local
// profiles appear here.
type knownHostKey struct{ Host, Port, Fingerprint string }
type knownHostEntry struct {
	Key                knownHostKey
	Profiles, Sessions int
}

func knownHostKeyFor(s connectionSettings) knownHostKey {
	return knownHostKey{s.Host, s.Port, s.Fingerprint}
}
func knownHostKeyID(k knownHostKey) string {
	hash := connectionDraftHash(k.Host, k.Port, k.Fingerprint)
	return hex.EncodeToString(hash[:])
}
func (d *desktop) knownHosts() []knownHostEntry {
	entries := make(map[knownHostKey]knownHostEntry)
	for _, p := range d.profiles {
		k := knownHostKeyFor(p.Settings)
		if !strings.HasPrefix(k.Fingerprint, "SHA256:") {
			continue
		}
		e := entries[k]
		e.Key = k
		e.Profiles++
		entries[k] = e
	}
	for _, s := range d.sessions {
		if s.shell == nil {
			continue
		}
		k := knownHostKeyFor(s.active)
		if !strings.HasPrefix(k.Fingerprint, "SHA256:") {
			continue
		}
		e := entries[k]
		e.Key = k
		e.Sessions++
		entries[k] = e
	}
	out := make([]knownHostEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Key, out[j].Key
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Fingerprint < b.Fingerprint
	})
	return out
}

func (d *desktop) forgetKnownHost(key knownHostKey) error {
	if d.busy {
		return errors.New("请等待当前操作完成")
	}
	if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		return errors.New("主机指纹无效")
	}
	found := false
	for _, entry := range d.knownHosts() {
		if entry.Key == key {
			found = true
			break
		}
	}
	if !found {
		return errors.New("该指纹已不存在")
	}
	before := d.profiles
	updated := append([]profile(nil), before...)
	for i := range updated {
		if knownHostKeyFor(updated[i].Settings) == key {
			updated[i].Settings.Fingerprint = ""
		}
	}
	d.profiles = updated
	// The revision check also prevents a stale window from erasing another
	// window's trust changes. A failed write leaves sessions untouched.
	if err := d.saveProfiles(); err != nil {
		d.profiles = before
		return err
	}
	for _, s := range d.sessions {
		if s.shell != nil && knownHostKeyFor(s.active) == key {
			s.active.Fingerprint = ""
		}
	}
	if knownHostKeyFor(d.active) == key {
		d.active.Fingerprint = ""
	}
	if d.settings.Host == key.Host && d.settings.Port == key.Port {
		if d.expectedFingerprint == key.Fingerprint {
			d.expectedFingerprint = ""
		}
		if d.fingerprint == key.Fingerprint {
			d.fingerprint = ""
		}
		if d.changedFingerprint == key.Fingerprint {
			d.changedFingerprint = ""
		}
	}
	return nil
}
