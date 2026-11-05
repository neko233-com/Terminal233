package main

import "github.com/egoist/mygo/ui"

// Small original line icons keep the navigation legible without relying on
// a platform-specific symbol font. Accessible names are supplied by buttons.
func workspaceIcon(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

var (
	serverIcon      = workspaceIcon(`<rect x="3" y="3" width="18" height="8" rx="2"/><rect x="3" y="13" width="18" height="8" rx="2"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/>`)
	terminalIcon    = workspaceIcon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3m6 0h4"/>`)
	fileIcon        = workspaceIcon(`<path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`)
	forwardIcon     = workspaceIcon(`<path d="M4 8h14m-4-4 4 4-4 4M20 16H6m4-4-4 4 4 4"/>`)
	cloudIcon       = workspaceIcon(`<path d="M7 18h10a4 4 0 0 0 .5-8A6 6 0 0 0 6 9a4.5 4.5 0 0 0 1 9z"/>`)
	starIcon        = workspaceIcon(`<path d="m12 3 2.7 5.5 6.1.9-4.4 4.3 1 6.1-5.4-2.9-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z"/>`)
	clockIcon       = workspaceIcon(`<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`)
	navigationIcons = map[string]*ui.SVG{
		"nav-connections": serverIcon,
		"nav-terminal":    terminalIcon,
		"nav-files":       fileIcon,
		"nav-forward":     forwardIcon,
		"nav-cloud":       cloudIcon,
		"group-all":       serverIcon,
		"group-favorites": starIcon,
		"group-recent":    clockIcon,
	}
)
