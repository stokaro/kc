package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// copyToPath is unexported and drives the F5 export; the atomic write itself
// is covered by internal/atomicfile. These tests check the wiring: a failed
// fetch stream keeps the old file, and a new export is private.

type brokenStream struct{ sent bool }

func (b *brokenStream) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errors.New("connection reset")
	}
	b.sent = true
	return copy(p, "partial"), nil
}

func (b *brokenStream) Close() error { return nil }

func TestCopyToPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		existing []byte
		stream   func() io.ReadCloser
		wantErr  bool
		wantData []byte
		wantMode os.FileMode
	}{
		{
			name:     "new secret export is private",
			stream:   func() io.ReadCloser { return io.NopCloser(bytes.NewReader([]byte("password"))) },
			wantData: []byte("password"),
			wantMode: 0o600,
		},
		{
			name:     "broken stream keeps the previous file",
			existing: []byte("old"),
			stream:   func() io.ReadCloser { return &brokenStream{} },
			wantErr:  true,
			wantData: []byte("old"),
			wantMode: 0o644,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dest := filepath.Join(t.TempDir(), "value.txt")
			if tc.existing != nil {
				if err := os.WriteFile(dest, tc.existing, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dest, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			a := &App{ctx: t.Context()}
			req := &copyRequest{
				subject:  "secret",
				filename: "value.txt",
				fetch:    func(context.Context) (io.ReadCloser, error) { return tc.stream(), nil },
			}
			err := a.copyToPath(req, dest)
			if (err != nil) != tc.wantErr {
				t.Fatalf("copyToPath() error = %v, wantErr %v", err, tc.wantErr)
			}
			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.wantData) {
				t.Errorf("content = %q, want %q", got, tc.wantData)
			}
			info, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.wantMode {
				t.Errorf("mode = %v, want %v", info.Mode().Perm(), tc.wantMode)
			}
		})
	}
}
