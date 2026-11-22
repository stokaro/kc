package navigation

import "slices"

// DefaultHistoryLimit is how many locations a History keeps when no limit
// is given.
const DefaultHistoryLimit = 50

// History is a panel's list of visited locations with a cursor, like the
// directory history of mc. Back and Forward move the cursor; Record adds a
// location after the cursor and drops the locations ahead of it. A location
// is kept once: recording it again moves it to the end. The zero value is
// ready to use.
type History struct {
	entries []Location
	index   int
	limit   int
}

// NewHistory returns a History that keeps at most limit locations; a limit
// below 1 means DefaultHistoryLimit.
func NewHistory(limit int) *History {
	return &History{limit: limit}
}

func (h *History) max() int {
	if h.limit < 1 {
		return DefaultHistoryLimit
	}
	return h.limit
}

// Record adds loc as the current location. Recording the current location
// again only refreshes its Path.
func (h *History) Record(loc Location) {
	loc.Steps = slices.Clone(loc.Steps)
	if cur, ok := h.Current(); ok && cur.Equal(loc) {
		h.entries[h.index].Path = loc.Path
		return
	}
	if len(h.entries) > 0 {
		h.entries = h.entries[:h.index+1]
	}
	h.entries = slices.DeleteFunc(h.entries, loc.Equal)
	h.entries = append(h.entries, loc)
	if over := len(h.entries) - h.max(); over > 0 {
		h.entries = slices.Delete(h.entries, 0, over)
	}
	h.index = len(h.entries) - 1
}

// Current returns the location at the cursor.
func (h *History) Current() (Location, bool) {
	if h.index < 0 || h.index >= len(h.entries) {
		return Location{}, false
	}
	return h.entries[h.index], true
}

// Back moves the cursor to the previous location and returns it.
func (h *History) Back() (Location, bool) {
	if h.index <= 0 || len(h.entries) == 0 {
		return Location{}, false
	}
	h.index--
	return h.entries[h.index], true
}

// Forward moves the cursor to the next location and returns it.
func (h *History) Forward() (Location, bool) {
	if h.index+1 >= len(h.entries) {
		return Location{}, false
	}
	h.index++
	return h.entries[h.index], true
}

// Go moves the cursor to entry i of Entries and returns that location.
func (h *History) Go(i int) (Location, bool) {
	if i < 0 || i >= len(h.entries) {
		return Location{}, false
	}
	h.index = i
	return h.entries[i], true
}

// Entries returns the locations, oldest first, and the cursor index.
func (h *History) Entries() ([]Location, int) {
	return slices.Clone(h.entries), h.index
}
