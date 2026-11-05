package main

import (
	"fmt"
	"testing"
)

func TestFileHistoryNavigationAndBranch(t *testing.T) {
	var h fileHistory
	if _, _, ok := h.target(-1); ok {
		t.Fatal("empty history can go back")
	}
	h.visit("/one")
	h.visit("/one")
	h.visit("/two")
	h.visit("/three")
	if len(h.paths) != 3 {
		t.Fatalf("refresh duplicated history: %#v", h.paths)
	}
	index, dir, ok := h.target(-1)
	if !ok || dir != "/two" {
		t.Fatalf("wrong back target: %q", dir)
	}
	// A failed read never commits the proposed target.
	if h.index != 2 || h.commitNavigation(index, "/unexpected") || h.index != 2 {
		t.Fatal("unconfirmed navigation changed history")
	}
	if !h.commitNavigation(index, dir) {
		t.Fatal("successful navigation was not committed")
	}
	if _, forward, ok := h.target(1); !ok || forward != "/three" {
		t.Fatal("forward target was lost")
	}
	h.visit("/new")
	if _, _, ok := h.target(1); ok || len(h.paths) != 3 || h.paths[2] != "/new" {
		t.Fatalf("new visit did not replace forward branch: %#v", h.paths)
	}
}

func TestFileHistoryIsBoundedAndSessionScoped(t *testing.T) {
	var first fileHistory
	for i := 0; i < maxFileHistory+2; i++ {
		first.visit(fmt.Sprint(i))
	}
	if len(first.paths) != maxFileHistory || first.paths[0] != "2" || first.index != maxFileHistory-1 {
		t.Fatalf("history bound failed: %#v", first)
	}
	var second fileHistory
	second.visit("other")
	d := &desktop{currentSession: 1, fileHistory: first, sessions: []*workspaceSession{{id: 1}, {id: 2, fileHistory: second}}}
	d.activateSession(2)
	if len(d.fileHistory.paths) != 1 || d.fileHistory.paths[0] != "other" {
		t.Fatal("second session did not restore its own history")
	}
	d.fileHistory.visit("new")
	d.activateSession(1)
	if len(d.fileHistory.paths) != maxFileHistory || d.fileHistory.paths[0] != "2" {
		t.Fatal("session switch overwrote first history")
	}
	d.activateSession(2)
	if len(d.fileHistory.paths) != 2 || d.fileHistory.paths[1] != "new" {
		t.Fatal("session switch did not save second history")
	}
}
