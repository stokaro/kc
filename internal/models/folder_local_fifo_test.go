//go:build unix

package models_test

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stokaro/kc/internal/models"
)

// A FIFO has no writer here: reading it would block forever.
func TestReadLocalTextFileRefusesFIFO(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := models.ReadLocalTextFile(p, 10); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("err = %v", err)
	}
	if err := models.CheckLocalEditable(p); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("CheckLocalEditable err = %v", err)
	}
}
