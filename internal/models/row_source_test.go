package models

import (
	"context"
	"errors"
	"testing"
	"time"

	table "github.com/stokaro/kc/internal/table"
)

// A failed populate is recorded and not run again by reads until its
// retry is due; then the source asks the folder to repaint. A change of
// the data (MarkDirty) retries at once.
func TestSliceRowSourceHoldsOffAFailedPopulate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newSliceRowSource(nil)
	var calls int
	fail := true
	src.setPopulate(func(context.Context) ([]table.Row, error) {
		calls++
		if fail {
			return nil, errors.New("server unavailable")
		}
		return []table.Row{&table.SimpleRow{ID: "row-1"}}, nil
	})
	retry := make(chan struct{}, 1)
	src.setRetryNotify(func() {
		select {
		case retry <- struct{}{}:
		default:
		}
	})

	for range 20 {
		src.Len(ctx)
		src.Lines(ctx, 0, 10)
	}
	if calls != 1 {
		t.Fatalf("40 reads after a failed populate ran it %d times, want once", calls)
	}
	if err := src.LoadError(); err == nil {
		t.Fatal("LoadError() = nil after a failed populate")
	}

	select {
	case <-retry:
	case <-time.After(5 * time.Second):
		t.Fatal("no repaint was asked for when the retry was due")
	}
	src.Len(ctx)
	if calls != 2 {
		t.Fatalf("a read after the retry was due ran populate %d times in all, want 2", calls)
	}

	fail = false
	src.MarkDirty()
	if n := src.Len(ctx); n != 1 || calls != 3 {
		t.Fatalf("after MarkDirty: Len() = %d with %d populates, want 1 row with 3", n, calls)
	}
	if err := src.LoadError(); err != nil {
		t.Fatalf("LoadError() = %v after a populate succeeded", err)
	}
}

func TestPopulateRetryDelay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, 250 * time.Millisecond},
		{2, 500 * time.Millisecond},
		{3, time.Second},
		{8, 30 * time.Second},
		{100, 30 * time.Second},
	}
	for _, tt := range tests {
		if got := populateRetryDelay(tt.failures); got != tt.want {
			t.Errorf("populateRetryDelay(%d) = %v, want %v", tt.failures, got, tt.want)
		}
	}
}

func TestSliceRowSourcePopulateWithDirtyDuringPopulate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	src := newSliceRowSource(nil)
	var populateCalls int
	src.setPopulate(func(context.Context) ([]table.Row, error) {
		populateCalls++
		if populateCalls == 1 {
			src.MarkDirty()
		}
		return []table.Row{&table.SimpleRow{ID: "row-1"}}, nil
	})

	idx, row, ok := src.Find(ctx, "row-1")
	if !ok {
		t.Fatalf("expected to find row")
	}
	if idx != 0 {
		t.Fatalf("expected index 0, got %d", idx)
	}
	if id, _, _, exists := row.Columns(); !exists || id != "row-1" {
		t.Fatalf("unexpected row columns; id=%q exists=%v", id, exists)
	}
	if populateCalls != 2 {
		t.Fatalf("expected populate to run twice, got %d", populateCalls)
	}
}
