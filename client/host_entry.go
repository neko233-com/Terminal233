package main

import (
	"errors"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

var errHostEntry = errors.New("请输入有效的 IP、主机名或 用户@主机")

// The initial host entry only prepares an untrusted form. It never runs input
// as a command, opens a network connection, or inherits saved credentials.
func parseHostEntry(input string) (connectionSettings, error) {
	if len(input) > 512 || !utf8.ValidString(input) || strings.IndexFunc(input, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return connectionSettings{}, errHostEntry
	}
	input = strings.TrimSpace(input)
	if looksLikeQuickConnection(input) {
		s, err := parseQuickConnection(input)
		if err != nil {
			return connectionSettings{}, errHostEntry
		}
		return s, nil
	}
	if strings.HasPrefix(input, "[") && strings.HasSuffix(input, "]") {
		input = input[1 : len(input)-1]
		if net.ParseIP(input) == nil {
			return connectionSettings{}, errHostEntry
		}
	}
	if net.ParseIP(input) == nil {
		if len(input) == 0 || len(input) > 253 {
			return connectionSettings{}, errHostEntry
		}
		for _, label := range strings.Split(strings.TrimSuffix(input, "."), ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return connectionSettings{}, errHostEntry
			}
			for _, r := range label {
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
					return connectionSettings{}, errHostEntry
				}
			}
		}
	}
	return connectionSettings{Host: input, Port: "22"}, nil
}

func (d *desktop) beginNewHost() bool {
	if d.busy || !d.prepareConnectionNavigation() {
		return false
	}
	d.settings = connectionSettings{Port: "22"}
	d.connectionJumpID, d.showConnectionJumpPicker = 0, false
	d.profileIdentityID = ""
	d.profileTagsCaretPending, d.profileGroupCaretPending = false, false
	d.profileSecretError = false
	d.showKeyOptions, d.showConnectionTools = false, false
	d.settingsFromCloud = false
	d.cloudFormSources, d.cloudFormVersions = nil, nil
	d.profileGroup = d.profileFilter
	d.profileLabel, d.profileTags = "", ""
	d.fingerprint, d.changedFingerprint, d.expectedFingerprint = "", "", ""
	d.selectedProfile, d.selectedCloudProfile = "", ""
	d.showProfileInspector = false
	d.profileInspectorAt = time.Time{}
	d.editConnection, d.connectionFocusPending = true, true
	d.connectionAuthFocusPending = false
	d.newHostEntryError = ""
	d.captureConnectionDraft()
	return true
}

func (d *desktop) continueHostEntry() {
	s, err := parseHostEntry(d.newHostEntry)
	if err != nil {
		d.newHostEntryError = err.Error()
		return
	}
	if !d.beginNewHost() {
		return
	}
	d.settings = s
	d.profileAutoDirty = true
	d.connectionFocusPending, d.connectionAuthFocusPending = false, true
	d.newHostEntry, d.newHostEntryError = "", ""
}

func (d *desktop) hostEntryVisible() bool {
	return len(d.profiles) == 0 && len(d.profileGroups) == 0 && d.cloud == nil && !d.editConnection &&
		d.profileSearch == "" && d.profileFilter == "" && !d.profileFavorites && !d.profileRecent && len(d.profileTagFilters) == 0 &&
		d.profileSource != "cloud" && !d.showCompactProfileTools && !d.showGroupControls &&
		!d.profileStoreConflict && d.cloudBatchCancel == nil
}

func (d *desktop) hostEntryView(c *ui.Context, width float32) {
	_, height := c.Size()
	iconSize, gap, padding := float32(60), float32(16), float32(24)
	if height < 650 {
		iconSize, gap, padding = 44, 10, 8
	}
	ui.Column(c.Key("host-onboarding")).Label("新建主机引导").Grow(1).FillWidth().Padding(padding, 8).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(min(float32(384), max(float32(1), width-64))).Gap(gap).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(iconSize).Height(iconSize).Radius(16).Background(c.Theme().SurfacePressed).AlignItems(ui.Center).Justify(ui.Center).Children(func() { ui.Icon(c, serverIcon).Width(26).Height(26).TextColor(c.Theme().Text) })
			ui.Text(c, "新建主机").FontSize(20).Bold()
			ui.Text(c, "保存主机信息，下次一键连接。").FontSize(13).TextColor(c.Theme().TextMuted).Wrap().TextAlign(ui.Center)
			ui.TextInput(c.Key("host-entry-address"), &d.newHostEntry).Label("新主机 IP 或主机名").Placeholder("输入 IP 或主机名").FillWidth().Height(38).AutoFocus().OnChange(func() { d.newHostEntryError = "" }).OnSubmit(d.continueHostEntry)
			if d.newHostEntryError != "" {
				ui.Text(c, d.newHostEntryError).Label("主机地址输入错误").FontSize(12).TextColor(c.Theme().Danger).Wrap()
			}
			if d.status != "" {
				ui.Text(c, d.status).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			}
			ui.PrimaryButton(c.Key("host-entry-continue"), "继续").Label("继续填写主机信息").FillWidth().Height(38).Disabled(d.busy || strings.TrimSpace(d.newHostEntry) == "").OnClick(d.continueHostEntry)
		})
	})
}
