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
	documentIcon    = workspaceIcon(`<path d="M6 3h8l4 4v14H6zM14 3v5h4"/>`)
	forwardIcon     = workspaceIcon(`<path d="M4 8h14m-4-4 4 4-4 4M20 16H6m4-4-4 4 4 4"/>`)
	cloudIcon       = workspaceIcon(`<path d="M7 18h10a4 4 0 0 0 .5-8A6 6 0 0 0 6 9a4.5 4.5 0 0 0 1 9z"/>`)
	snippetIcon     = workspaceIcon(`<path d="M9 4C7 4 7 6 7 8v1c0 2-1 3-3 3 2 0 3 1 3 3v1c0 2 0 4 2 4M15 4c2 0 2 2 2 4v1c0 2 1 3 3 3-2 0-3 1-3 3v1c0 2 0 4-2 4"/>`)
	starIcon        = workspaceIcon(`<path d="m12 3 2.7 5.5 6.1.9-4.4 4.3 1 6.1-5.4-2.9-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z"/>`)
	clockIcon       = workspaceIcon(`<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`)
	searchIcon      = workspaceIcon(`<circle cx="10.5" cy="10.5" r="6.5"/><path d="m15.5 15.5 5 5"/>`)
	splitIcon       = workspaceIcon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M12 4v16"/>`)
	monitorIcon     = workspaceIcon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M5 13h3l2-4 3 7 2-4h4"/>`)
	plusIcon        = workspaceIcon(`<path d="M12 4v16M4 12h16"/>`)
	closeIcon       = workspaceIcon(`<path d="m6 6 12 12M18 6 6 18"/>`)
	settingsIcon    = workspaceIcon(`<path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.4 1.4m11.2 11.2L19 19M19 5l-1.4 1.4M6.4 17.6 5 19"/><circle cx="12" cy="12" r="4"/><circle cx="12" cy="12" r="7"/>`)
	moreIcon        = workspaceIcon(`<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>`)
	navigationIcons = map[string]*ui.SVG{
		"nav-connections": serverIcon,
		"nav-terminal":    terminalIcon,
		"nav-files":       fileIcon,
		"nav-forward":     forwardIcon,
		"nav-cloud":       cloudIcon,
		"nav-snippets":    snippetIcon,
		"group-all":       serverIcon,
		"group-favorites": starIcon,
		"group-recent":    clockIcon,
	}
)
