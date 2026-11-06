package main

func validPaneWidths(navigation, inspector, quick float32) bool {
	return (navigation == 0 || (navigation >= 170 && navigation <= 360)) &&
		(inspector == 0 || (inspector >= 320 && inspector <= 560)) &&
		(quick == 0 || (quick >= 280 && quick <= 520))
}

// Layout choices never contain connection data. A zero value uses the
// original default; displayed sizes clamp to the current window's room.
func (d *desktop) navigationPanelWidth(windowWidth float32) float32 {
	width := d.navigationWidth
	if width == 0 {
		width = workspaceSidebarWidth(windowWidth)
	}
	return max(float32(170), min(min(float32(360), windowWidth-430), width))
}

func (d *desktop) workspaceContentWidth(windowWidth float32) float32 {
	if !d.sidebarVisible() {
		return windowWidth
	}
	windowWidth -= d.sidebarWidth(windowWidth)
	if d.railExpanded {
		windowWidth-- // the native splitter line
	}
	return windowWidth
}

func (d *desktop) inspectorPanelWidth(windowWidth float32) float32 {
	width := d.inspectorWidth
	if width == 0 {
		width = connectionInspectorWidth
	}
	limit := min(float32(560), d.workspaceContentWidth(windowWidth)-401)
	return max(float32(320), min(limit, width))
}

func (d *desktop) quickCommandsPanelWidth(workspaceWidth float32) float32 {
	width := d.quickPanelWidth
	if width == 0 {
		width = 340
	}
	return max(float32(280), min(min(float32(520), workspaceWidth-521), width))
}

func fitPanePercent(value, room, firstMinimum, lastMinimum float32) float32 {
	if room <= 1 {
		return value
	}
	room--
	if minimum := firstMinimum + lastMinimum; room < minimum {
		firstMinimum *= room / minimum
		lastMinimum *= room / minimum
	}
	low := firstMinimum * 100 / room
	high := 100 - lastMinimum*100/room
	return max(low, min(high, value))
}
