package ui

import (
	"context"
	"fmt"
	"testing"
)

func TestReadErrorText(t *testing.T) {
	err := fmt.Errorf("could not read pods in default: %w", context.DeadlineExceeded)
	if got := readErrorText(err); got != "Could not read pods in default: timed out. Try again." {
		t.Fatalf("got %q", got)
	}
}

func TestReportReadErrorNeverBlocks(t *testing.T) {
	app := &App{readErrCh: make(chan error, 1)}
	app.reportReadError(fmt.Errorf("a"))
	app.reportReadError(fmt.Errorf("b")) // channel full: dropped, not blocked
	if len(app.readErrCh) != 1 {
		t.Fatalf("expected one queued error")
	}
}
