package models

import (
	"context"
	"sync"
	"time"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
)

// rowSource supplies row windows for a folder. Implementations may cache rows
// or stream them directly from informer-backed stores.
type rowSource interface {
	Lines(ctx context.Context, top, num int) []table.Row
	Above(ctx context.Context, id string, n int) []table.Row
	Below(ctx context.Context, id string, n int) []table.Row
	Len(ctx context.Context) int
	Find(ctx context.Context, id string) (int, table.Row, bool)
	ItemByID(ctx context.Context, id string) (Item, bool)
	MarkDirty()
}

// sliceRowSource maintains an in-memory snapshot of rows computed on demand via
// a populate callback. It replaces the previous table.SliceList usage while
// keeping cached folders simple for now.
//
// populate runs on the goroutine that reads the rows, the UI's for a panel.
// A failed populate is recorded and not run again by reads until a backoff
// delay has passed; then onRetry tells the folder, whose repaint retries.
// MarkDirty, a change of the data, retries at the next read.
type sliceRowSource struct {
	populate func(context.Context) ([]table.Row, error)

	mu    sync.Mutex
	rows  []table.Row
	index map[string]int
	items map[string]Item
	dirty bool
	once  sync.Once

	// failure is the error of the last populate when it failed, and
	// failures the number of failed populates in a row. Reads serve the
	// previous rows until retryAt.
	failure    error
	failures   int
	retryAt    time.Time
	retryTimer *time.Timer
	// onRetry repaints the folder when a retry is due; nil when the
	// source has no folder.
	onRetry func()
}

// populateRetryMin is the wait after a failed populate. It doubles with
// each further failure, up to populateRetryMax.
const (
	populateRetryMin = 250 * time.Millisecond
	populateRetryMax = 30 * time.Second
)

// populateRetryDelay is the wait after the given number of failed
// populates in a row.
func populateRetryDelay(failures int) time.Duration {
	d := populateRetryMin
	for range failures - 1 {
		if d >= populateRetryMax {
			break
		}
		d *= 2
	}
	return min(d, populateRetryMax)
}

func newSliceRowSource(populate func(context.Context) ([]table.Row, error)) *sliceRowSource {
	return &sliceRowSource{populate: populate, dirty: true}
}

func (s *sliceRowSource) setPopulate(populate func(context.Context) ([]table.Row, error)) {
	s.mu.Lock()
	s.populate = populate
	s.dirty = true
	s.clearRetryLocked()
	s.mu.Unlock()
}

// setRetryNotify sets the function that repaints the folder when a failed
// populate may run again.
func (s *sliceRowSource) setRetryNotify(fn func()) {
	s.mu.Lock()
	s.onRetry = fn
	s.mu.Unlock()
}

// LoadError is the error of the last populate when it failed, else nil.
func (s *sliceRowSource) LoadError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// listProblem is the error of the last populate when it failed and left
// the list empty. A failure after rows were read keeps those rows and is
// no reason for an empty list.
func (s *sliceRowSource) listProblem() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) > 0 {
		return nil
	}
	return s.failure
}

func (s *sliceRowSource) ensureLocked(ctx context.Context) {
	s.once.Do(func() { s.dirty = true })
	for {
		if !s.dirty {
			return
		}
		if !s.retryAt.IsZero() && time.Now().Before(s.retryAt) {
			// A populate failed a moment ago; the retry timer repaints.
			return
		}
		populate := s.populate
		if populate == nil {
			s.rows = nil
			s.index = nil
			s.items = nil
			s.dirty = false
			return
		}
		s.dirty = false
		s.mu.Unlock()
		rows, err := populate(ctx)
		s.mu.Lock()
		if err != nil {
			// Keep the previous rows, and do not run populate again on
			// every read: a failing populate may wait for a server each
			// time.
			s.dirty = true
			s.failedLocked(ctx, err)
			return
		}
		s.failure, s.failures = nil, 0
		s.clearRetryLocked()
		if s.dirty {
			// Dirty again while unlocked; refresh with the latest data.
			continue
		}
		s.rows = rows
		s.rebuildIndexLocked()
		return
	}
}

// failedLocked records err and holds populate off until the backoff delay
// has passed, then repaints the folder.
func (s *sliceRowSource) failedLocked(ctx context.Context, err error) {
	s.failure = err
	s.failures++
	delay := populateRetryDelay(s.failures)
	s.retryAt = time.Now().Add(delay)
	if s.retryTimer != nil {
		s.retryTimer.Stop()
		s.retryTimer = nil
	}
	ctrllog.FromContext(ctx).WithName("rows").Info("populate failed; retrying later", "error", err.Error(), "failures", s.failures, "retryIn", delay.String())
	if s.onRetry != nil {
		s.retryTimer = time.AfterFunc(delay, s.retryDue)
	}
}

func (s *sliceRowSource) retryDue() {
	s.mu.Lock()
	s.retryAt = time.Time{}
	s.retryTimer = nil
	notify := s.onRetry
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}

func (s *sliceRowSource) clearRetryLocked() {
	s.retryAt = time.Time{}
	if s.retryTimer != nil {
		s.retryTimer.Stop()
		s.retryTimer = nil
	}
}

func (s *sliceRowSource) rebuildIndexLocked() {
	s.index = make(map[string]int, len(s.rows))
	s.items = make(map[string]Item, len(s.rows))
	for i, row := range s.rows {
		if row == nil {
			continue
		}
		id, _, _, ok := row.Columns()
		if !ok {
			continue
		}
		s.index[id] = i
		if item, ok := row.(Item); ok {
			s.items[id] = item
		}
	}
}

func (s *sliceRowSource) Lines(ctx context.Context, top, num int) []table.Row {
	if num <= 0 {
		return nil
	}
	s.mu.Lock()
	s.ensureLocked(ctx)
	rows := s.rows
	s.mu.Unlock()
	if len(rows) == 0 || top >= len(rows) {
		return nil
	}
	if top < 0 {
		top = 0
	}
	end := min(top+num, len(rows))
	return rows[top:end]
}

func (s *sliceRowSource) Above(ctx context.Context, id string, n int) []table.Row {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	s.ensureLocked(ctx)
	idx, ok := s.index[id]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	start := max(idx-n, 0)
	rows := append([]table.Row(nil), s.rows[start:idx]...)
	s.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func (s *sliceRowSource) Below(ctx context.Context, id string, n int) []table.Row {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	s.ensureLocked(ctx)
	idx, ok := s.index[id]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	start := idx + 1
	if start >= len(s.rows) {
		s.mu.Unlock()
		return nil
	}
	end := min(start+n, len(s.rows))
	rows := append([]table.Row(nil), s.rows[start:end]...)
	s.mu.Unlock()
	return rows
}

func (s *sliceRowSource) Len(ctx context.Context) int {
	s.mu.Lock()
	s.ensureLocked(ctx)
	ln := len(s.rows)
	s.mu.Unlock()
	return ln
}

func (s *sliceRowSource) Find(ctx context.Context, id string) (int, table.Row, bool) {
	s.mu.Lock()
	s.ensureLocked(ctx)
	idx, ok := s.index[id]
	if !ok || idx < 0 || idx >= len(s.rows) {
		s.mu.Unlock()
		return -1, nil, false
	}
	row := s.rows[idx]
	s.mu.Unlock()
	return idx, row, true
}

func (s *sliceRowSource) ItemByID(ctx context.Context, id string) (Item, bool) {
	s.mu.Lock()
	s.ensureLocked(ctx)
	it, ok := s.items[id]
	s.mu.Unlock()
	return it, ok
}

// MarkDirty says the data changed. The next read runs populate, even
// right after a failure: the change may be what it waited for.
func (s *sliceRowSource) MarkDirty() {
	s.mu.Lock()
	s.dirty = true
	s.clearRetryLocked()
	s.mu.Unlock()
}
