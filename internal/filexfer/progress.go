package filexfer

import (
	"sync"
	"sync/atomic"
)

// Counter tracks the progress of a copy. The copy updates it from its own
// goroutine; the UI reads Snapshot from another one. A nil Counter counts
// nothing.
type Counter struct {
	files atomic.Int64
	bytes atomic.Int64

	mu      sync.Mutex
	current string
	index   int
	total   int
}

// Progress is a point-in-time view of a Counter.
type Progress struct {
	// Files is the number of files written completely.
	Files int64
	// Bytes is the number of content bytes received, including those of
	// the file being written.
	Bytes int64
	// Current is the top-level entry being copied, Index its 1-based
	// position and Total the number of entries.
	Current      string
	Index, Total int
}

// Snapshot returns the current progress.
func (c *Counter) Snapshot() Progress {
	if c == nil {
		return Progress{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return Progress{
		Files:   c.files.Load(),
		Bytes:   c.bytes.Load(),
		Current: c.current,
		Index:   c.index,
		Total:   c.total,
	}
}

func (c *Counter) addBytes(n int64) {
	if c != nil && n > 0 {
		c.bytes.Add(n)
	}
}

func (c *Counter) addFile() {
	if c != nil {
		c.files.Add(1)
	}
}

func (c *Counter) startEntry(name string, index, total int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.current, c.index, c.total = name, index, total
	c.mu.Unlock()
}
