package ui

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
)

func TestReadErrorText(t *testing.T) {
	err := fmt.Errorf("could not read pods in default: %w", context.DeadlineExceeded)
	if got := readErrorText(err); got != "Could not read pods in default: timed out. Try again." {
		t.Fatalf("got %q", got)
	}
}

// A list's read failure is said in plain words after what could not be
// read; the URL and the dial details are dropped.
func TestReadErrorTextOfAListRead(t *testing.T) {
	refused := &url.Error{Op: "Get", URL: "https://10.0.0.1:6443/api/v1/namespaces/shop/pods?limit=500", Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	err := &models.ReadError{What: "pods in shop", Err: refused}
	want := "Could not read pods in shop: cannot reach 10.0.0.1:6443: connection refused. Try again."
	if got := readErrorText(err); got != want {
		t.Fatalf("readErrorText = %q, want %q", got, want)
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

func TestWaitLoopMessagesPassModals(t *testing.T) {
	for _, msg := range []tea.Msg{readErrorMsg{}, FolderDirtyMsg{}, DiscoveryRefreshedMsg{}} {
		if !passesModal(msg) {
			t.Errorf("%T must reach the app with a modal open, or its wait loop stops", msg)
		}
	}
	if passesModal(tea.KeyPressMsg{Code: 'a'}) {
		t.Errorf("keys belong to the modal")
	}
}
