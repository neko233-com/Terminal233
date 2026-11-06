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
	editIcon        = workspaceIcon(`<path d="m4 20 4-.8L19 8a2.1 2.1 0 0 0-3-3L5 16zM14 7l3 3"/>`)
	trashIcon       = workspaceIcon(`<path d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 11v5M14 11v5"/>`)
	plusIcon        = workspaceIcon(`<path d="M12 4v16M4 12h16"/>`)
	broadcastIcon   = workspaceIcon(`<circle cx="5" cy="12" r="2"/><circle cx="19" cy="5" r="2"/><circle cx="19" cy="12" r="2"/><circle cx="19" cy="19" r="2"/><path d="M7 12h10M7 11l10-5M7 13l10 5"/>`)
	closeIcon       = workspaceIcon(`<path d="m6 6 12 12M18 6 6 18"/>`)
	settingsIcon    = workspaceIcon(`<path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.4 1.4m11.2 11.2L19 19M19 5l-1.4 1.4M6.4 17.6 5 19"/><circle cx="12" cy="12" r="4"/><circle cx="12" cy="12" r="7"/>`)
	identityIcon    = workspaceIcon(`<circle cx="8" cy="10" r="3"/><path d="M11 10h10m-3 0v3m-3-3v2M8 13v5"/>`)
	knownHostIcon   = workspaceIcon(`<path d="M5 11a7 7 0 0 1 14 0c0 3-1 6-2 8M8 12a4 4 0 0 1 8 0c0 3-.5 6-1.5 8M11 12a1 1 0 0 1 2 0c0 4-.5 7-1 9M5 14c0 2-.3 3.5-1 5M8 15c0 2-.4 4-1 5"/>`)
	moreIcon        = workspaceIcon(`<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>`)
	railCompactIcon = workspaceIcon(`<path d="M4 4v16M10 7l-4 5 4 5M20 4v16"/>`)
	railExpandIcon  = workspaceIcon(`<path d="M4 4v16M14 7l4 5-4 5M20 4v16"/>`)
	navigationIcons = map[string]*ui.SVG{
		"nav-connections": serverIcon,
		"nav-terminal":    terminalIcon,
		"nav-files":       fileIcon,
		"nav-forward":     forwardIcon,
		"nav-cloud":       cloudIcon,
		"nav-snippets":    snippetIcon,
		"nav-identities":  identityIcon,
		"nav-known-hosts": knownHostIcon,
		"group-all":       serverIcon,
		"group-favorites": starIcon,
		"group-recent":    clockIcon,
	}
)
