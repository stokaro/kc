package podfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	// ErrShellMissing indicates the container image lacks a usable /bin/sh.
	ErrShellMissing = errors.New("pod filesystem: shell missing")
	// ErrHelperNeeded means the container can only be browsed through a
	// debug helper, which needs the user's consent (SessionSpec.AllowHelper).
	ErrHelperNeeded = errors.New("pod filesystem: debug helper needed")
)

// MissingCommandError reports that a required binary is absent in the container image.
type MissingCommandError struct {
	Command string
}

func (e MissingCommandError) Error() string {
	return fmt.Sprintf("pod filesystem: missing required command %s", e.Command)
}

// EntryType classifies filesystem entries returned by ExecSession.List.
type EntryType string

const (
	EntryTypeFile    EntryType = "file"
	EntryTypeDir     EntryType = "dir"
	EntryTypeSymlink EntryType = "symlink"
	EntryTypeSocket  EntryType = "socket"
	EntryTypePipe    EntryType = "pipe"
	EntryTypeDevice  EntryType = "device"
	EntryTypeOther   EntryType = "other"
)

// FileEntry is a portable description of a remote filesystem entry.
type FileEntry struct {
	Name      string
	Path      string
	Type      EntryType
	Size      int64
	Mode      uint32
	UpdatedAt time.Time
	Target    string // symlink target if available
}

// SessionSpec identifies the container to exec into.
type SessionSpec struct {
	Namespace string
	Pod       string
	Container string
	// AllowHelper permits adding a debug helper when the container has no
	// shell. Adding one changes the pod for good, so the user must agree.
	AllowHelper bool
	// Heartbeat is a file each command touches; the helper exits when it
	// has not been touched for helperIdleTimeout.
	Heartbeat string
}

// ExecSession provides filesystem operations backed by a long-lived exec stream.
type ExecSession interface {
	List(ctx context.Context, path string) ([]FileEntry, error)
	ReadFile(ctx context.Context, path string, limit int64) (io.ReadCloser, error)
	Close() error
}

// Factory constructs ExecSessions on demand. ctx bounds establishing the
// session; the session stays usable after ctx ends, until Close.
type Factory interface {
	NewSession(ctx context.Context, spec SessionSpec) (ExecSession, error)
}

// HelperUsage indicates whether an ExecSession relies on a debug helper container.
type HelperUsage interface {
	HelperUsed() bool
}
