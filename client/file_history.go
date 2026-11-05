package main

// Directory history belongs to a single terminal session and is kept in memory.
type fileHistory struct {
	paths []string
	index int
}

const maxFileHistory = 100

func (h *fileHistory) target(offset int) (int, string, bool) {
	i := h.index + offset
	if len(h.paths) == 0 || i < 0 || i >= len(h.paths) {
		return 0, "", false
	}
	return i, h.paths[i], true
}

// Only successful reads are recorded. A normal visit after going back drops
// the forward branch, while refreshing the current directory adds nothing.
func (h *fileHistory) visit(dir string) {
	if len(h.paths) != 0 && h.paths[h.index] == dir {
		return
	}
	if len(h.paths) != 0 {
		h.paths = h.paths[:h.index+1]
	}
	h.paths = append(h.paths, dir)
	if len(h.paths) > maxFileHistory {
		h.paths = append([]string(nil), h.paths[len(h.paths)-maxFileHistory:]...)
	}
	h.index = len(h.paths) - 1
}

func (h *fileHistory) commitNavigation(index int, dir string) bool {
	if index < 0 || index >= len(h.paths) || h.paths[index] != dir {
		return false
	}
	h.index = index
	return true
}
