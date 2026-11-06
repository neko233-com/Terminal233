package main

import "github.com/egoist/mygo/ui"

func (d *desktop) hostLayoutGap() float32 {
	if d.hostListView {
		return 4
	}
	return 10
}

func (d *desktop) hostLayoutCard(c *ui.Context, width float32, selected bool) ui.Element {
	card := ui.Column(c).Width(width).Padding(10).Gap(6).Background(c.Theme().Surface).Radius(10).BorderWidth(1).BorderColor(ui.Transparent)
	if d.hostListView {
		card = card.Padding(4, 8).Gap(4).Radius(6).Background(ui.Transparent)
	}
	if selected {
		card = card.Background(c.Theme().SurfaceHover).BorderColor(c.Theme().Accent)
	} else if card.Hovered() {
		card = card.Background(c.Theme().SurfaceHover)
	}
	return card
}

func hostAddressColumn(width float32) float32 {
	return min(float32(300), max(float32(180), (width-80)*.38))
}

// Only the presentation differs. The surrounding host element retains the
// same identity, click, drag, context menu and credential/trust behavior.
func (d *desktop) hostSummary(c *ui.Context, icon *ui.SVG, name, address, user string, width float32) {
	size, glyph := float32(38), float32(19)
	if d.hostListView {
		size, glyph = 28, 16
	}
	ui.Box(c).Width(size).Height(size).Shrink(0).Radius(8).Background(c.Theme().SurfacePressed).AlignItems(ui.Center).Justify(ui.Center).Children(func() { ui.Icon(c, icon).Width(glyph).Height(glyph).TextColor(c.Theme().Text) })
	if d.hostListView && width >= 620 {
		ui.Text(c, name).FontSize(13).Grow(1).SingleLine().Tooltip(name)
		ui.Text(c, address).FontSize(12).TextColor(c.Theme().TextMuted).Width(hostAddressColumn(width)).SingleLine().Tooltip(address)
		if user == "" {
			user = "—"
		}
		ui.Text(c, user).FontSize(12).TextColor(c.Theme().TextMuted).Width(100).SingleLine().Tooltip(user)
		return
	}
	ui.Column(c).Grow(1).Gap(3).Children(func() {
		ui.Text(c, name).FontSize(14).SingleLine().Tooltip(name)
		detail := address
		if user != "" {
			detail = user + "@" + address
		}
		ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(detail)
	})
}

func (d *desktop) toggleHostSort(column string) {
	if d.profileRecent {
		return
	}
	if d.profileSort == column {
		d.profileSortDescending = !d.profileSortDescending
	} else {
		d.profileSort, d.profileSortDescending = column, false
	}
}

func (d *desktop) hostListHeader(c *ui.Context, width float32, source string) {
	if !d.hostListView || width < 620 {
		return
	}
	headerLabel := "本机主机列表列标题"
	if source == "cloud" {
		headerLabel = "云端主机列表列标题"
	}
	ui.Row(c.Key("host-list-header-"+source)).Label(headerLabel).FillWidth().Padding(6, 12).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(28).Shrink(0)
		for _, col := range []struct {
			key, label string
			width      float32
		}{{"name", "名称", 0}, {"host", "地址", hostAddressColumn(width)}} {
			column := col
			label := col.label
			if d.profileSort == col.key && !d.profileRecent {
				if d.profileSortDescending {
					label += " ↓"
				} else {
					label += " ↑"
				}
			}
			scope := "本机"
			if source == "cloud" {
				scope = "云端"
			}
			b := ui.Button(c.Key("host-list-sort-"+source+"-"+col.key), label).Label(scope + "按" + col.label + "排序").FontSize(12).TextColor(c.Theme().TextMuted).BorderWidth(0).Padding(0).Background(ui.Transparent).Justify(ui.Start).Disabled(d.profileRecent)
			if col.width == 0 {
				b = b.Grow(1)
			} else {
				b = b.Width(col.width)
			}
			b.OnClick(func() { d.toggleHostSort(column.key); c.Invalidate() })
		}
		ui.Text(c, "用户").FontSize(12).TextColor(c.Theme().TextMuted).Width(100)
	})
}
