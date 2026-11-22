package atomicfile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/atomicfile"
)

func TestWriteIn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ro.txt"), []byte("ro"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := atomicfile.WriteIn(t.Context(), root, "sub/new.txt", 0o750, strings.NewReader("data")); err != nil {
		t.Fatalf("WriteIn: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "sub", "new.txt"))
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("new file = %v, %v; want mode 0750", info, err)
	}

	for _, tc := range []struct {
		name string
		want error
	}{{"ro.txt", atomicfile.ErrReadOnly}, {"link", atomicfile.ErrSymlink}, {"sub", atomicfile.ErrNotRegular}} {
		if err := atomicfile.WriteIn(t.Context(), root, tc.name, 0o600, strings.NewReader("x")); !errors.Is(err, tc.want) {
			t.Errorf("WriteIn(%s) = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := atomicfile.WriteIn(t.Context(), root, "../escape.txt", 0o600, strings.NewReader("x")); err == nil {
		t.Errorf("WriteIn outside the root succeeded")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := atomicfile.WriteIn(ctx, root, "canceled.txt", 0o600, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled WriteIn = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".kc-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}
