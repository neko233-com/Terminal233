package main

import (
	"path/filepath"
	"testing"

	"go.bug.st/serial"
)

func TestSerialConfigurationValidation(t *testing.T) {
	draft := defaultSerialDraft()
	draft.Port = " com12 "
	settings, err := draft.settings()
	if err != nil || settings.Port != "COM12" {
		t.Fatalf("valid settings rejected: %v", err)
	}
	mode, err := settings.mode()
	if err != nil || mode.BaudRate != 115200 || mode.DataBits != 8 || mode.Parity != serial.NoParity || mode.StopBits != serial.OneStopBit || mode.InitialStatusBits == nil || mode.InitialStatusBits.DTR || mode.InitialStatusBits.RTS {
		t.Fatalf("unexpected default serial mode: %#v %v", mode, err)
	}
	for _, bad := range []serialSettings{
		{Port: `\\.\COM1`, BaudRate: 9600, DataBits: 8, Parity: "None", StopBits: "1"},
		{Port: "COM0", BaudRate: 9600, DataBits: 8, Parity: "None", StopBits: "1"},
		{Port: "COM01", BaudRate: 9600, DataBits: 8, Parity: "None", StopBits: "1"},
		{Port: "COM1", BaudRate: 0, DataBits: 8, Parity: "None", StopBits: "1"},
		{Port: "COM1", BaudRate: 9600, DataBits: 8, Parity: "invalid", StopBits: "1"},
		{Port: "COM1", BaudRate: 9600, DataBits: 8, Parity: "None", StopBits: "1.5"},
		{Port: "COM1", BaudRate: 9600, DataBits: 5, Parity: "None", StopBits: "2"},
	} {
		if _, err := bad.mode(); err == nil {
			t.Fatalf("invalid serial settings accepted: %#v", bad)
		}
	}
}

func TestSerialWorkspacePersistenceAndIsolation(t *testing.T) {
	settings := serialSettings{Port: "COM7", BaudRate: 9600, DataBits: 8, Parity: "Even", StopBits: "1", DTR: true}
	w := savedWorkspace{ID: "0123456789abcdef0123456789abcdef", Name: "serial", Active: 0, SplitFirst: -1, SplitSecond: -1, Entries: []workspaceTemplateEntry{{Kind: "serial", Label: "串口 · COM7", Serial: &settings}}}
	if err := validateSavedWorkspace(w); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workspaces.json")
	if _, err := updateWorkspaceTemplates(path, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil }); err != nil {
		t.Fatal(err)
	}
	file, err := readWorkspaceTemplates(path)
	if err != nil || file.Version != 6 || len(file.Items) != 1 || *file.Items[0].Entries[0].Serial != settings {
		t.Fatalf("serial settings did not round-trip: %v", err)
	}
	copy := cloneSavedWorkspace(file.Items[0])
	copy.Entries[0].Serial.Port = "COM8"
	if file.Items[0].Entries[0].Serial.Port != "COM7" {
		t.Fatal("workspace clone shared serial settings")
	}
	var d desktop
	if _, err := d.resolveWorkspace(w); err != nil {
		t.Fatalf("serial workspace did not resolve: %v", err)
	}
	w.Entries = append(w.Entries, w.Entries[0])
	if _, err := d.resolveWorkspace(w); err == nil {
		t.Fatal("duplicate exclusive serial ports were accepted")
	}
	w.Entries = w.Entries[:1]
	cloud, err := newCloudClient("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := portableWorkspace(w, cloud, "user"); err == nil {
		t.Fatal("device-local serial settings were silently copied to cloud")
	}
}
