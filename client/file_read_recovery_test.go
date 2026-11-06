package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFileReadFailureRestoresPreviousDirectory(t *testing.T) {
	d := &desktop{currentSession: 4, fileDir: "/denied", busy: true, files: []remoteFile{{Name: "stale"}}, selectedFile: "stale", fileChecked: map[string]bool{"stale": true}}
	d.fileHistory.visit("/home")
	d.fileHistory.visit("/work")
	d.fileHistory.visit("/last")
	if !d.finishFileDirectoryRead(4, nil, "/denied", "", -1, nil, errors.New("permission denied"), true) {
		t.Fatal("failed navigation did not request recovery")
	}
	if d.fileDir != "/last" || d.fileHistory.index != 2 || len(d.files) != 0 || d.selectedFile != "" || len(d.fileChecked) != 0 {
		t.Fatal("recovery changed history or left a stale selection")
	}
	message := d.fileStatus
	if !strings.Contains(message, "permission denied") {
		t.Fatal("original failure was lost")
	}
	if d.finishFileDirectoryRead(4, nil, "/last", message, -1, []remoteFile{{Name: "fresh"}}, nil, false) {
		t.Fatal("successful recovery requested another read")
	}
	if len(d.files) != 1 || d.files[0].Name != "fresh" || d.fileHistory.index != 2 || !strings.Contains(d.fileStatus, "permission denied") {
		t.Fatal("recovery did not preserve the error and freshly loaded directory")
	}
}

func TestFileReadRecoveryStopsAndIgnoresObsoleteResults(t *testing.T) {
	d := &desktop{currentSession: 2, fileDir: "/current", busy: true, files: []remoteFile{{Name: "visible"}}}
	d.fileHistory.visit("/current")
	if d.finishFileDirectoryRead(1, nil, "/other", "", -1, nil, errors.New("old"), true) || d.fileDir != "/current" || len(d.files) != 1 {
		t.Fatal("another session's result changed the active directory")
	}
	if d.finishFileDirectoryRead(2, nil, "/old", "", -1, nil, errors.New("old"), true) || d.fileDir != "/current" || len(d.files) != 1 {
		t.Fatal("an outdated request replaced a newer path")
	}
	if d.finishFileDirectoryRead(2, nil, "/current", "prior failure", -1, nil, errors.New("read failed"), false) {
		t.Fatal("failed recovery retried indefinitely")
	}
	if len(d.files) != 0 || !strings.Contains(d.fileStatus, "prior failure") || !strings.Contains(d.fileStatus, "read failed") {
		t.Fatal("failed recovery retained stale files or hid an error")
	}
}

func TestLocalFileRefreshFailureClearsStaleEntries(t *testing.T) {
	d := &desktop{localPane: localFilePane{directory: `C:\work`, input: `C:\work`, generation: 7, loading: true, files: []remoteFile{{Name: "stale"}}, selected: "stale", paneSelection: paneSelection{names: map[string]bool{"stale": true}}}}
	d.finishLocalPaneLoad(7, `C:\work`, -1, nil, errors.New("read failed"))
	if d.localPane.loading || len(d.localPane.files) != 0 || d.localPane.selected != "" || len(d.localPane.paneSelection.names) != 0 || !strings.Contains(d.localPane.status, "read failed") {
		t.Fatal("failed refresh retained a stale local file selection")
	}
}
