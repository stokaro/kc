package models

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalOpenFileView(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	small := write("small.txt", []byte("hello\n"))
	exact := write("exact.txt", bytes.Repeat([]byte("a"), LocalViewWholeBytes))
	large := write("large.txt", bytes.Repeat([]byte("b\n"), LocalViewWholeBytes))
	binary := write("blob.bin", append(bytes.Repeat([]byte("c"), LocalViewWholeBytes*2), 0))
	head := write("head.bin", append([]byte("x\x00"), bytes.Repeat([]byte("d"), LocalViewWholeBytes*2)...))
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		path      string
		wantPaged bool
		wantErr   string
	}{
		{name: "small file whole", path: small},
		{name: "at the limit whole", path: exact},
		{name: "large file paged", path: large, wantPaged: true},
		// The sniff covers the first bytes, as before: a NUL further on
		// is shown in pages like any text.
		{name: "late NUL paged", path: binary, wantPaged: true},
		{name: "binary refused", path: head, wantErr: "is a binary file"},
		{name: "directory refused", path: filepath.Join(dir, "sub"), wantErr: "is a directory"},
		{name: "missing refused", path: filepath.Join(dir, "gone"), wantErr: "cannot read gone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := &LocalFileItem{path: tc.path}
			view, err := item.OpenFileView(t.Context())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(tc.path)
			if view.Size != info.Size() || view.Title != tc.path || view.Filename != filepath.Base(tc.path) {
				t.Fatalf("view = %+v", view)
			}
			if (view.Source != nil) != tc.wantPaged {
				t.Fatalf("paged = %v, want %v", view.Source != nil, tc.wantPaged)
			}
			if view.Source == nil {
				if int64(len(view.Body)) != info.Size() {
					t.Fatalf("body has %d bytes, want %d", len(view.Body), info.Size())
				}
				return
			}
			buf := make([]byte, 4)
			if n, err := view.Source.ReadAt(t.Context(), buf, 0); n != 4 || err != nil {
				t.Fatalf("ReadAt = %d, %v", n, err)
			}
			if err := view.Source.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}
