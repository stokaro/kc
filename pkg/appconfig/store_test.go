package appconfig_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	yaml "sigs.k8s.io/yaml"

	"github.com/stokaro/kc/pkg/appconfig"
)

const customConfig = `viewer:
  theme: monokai
  mode: wrap
panel:
  width:
    leftPercent: 30
    rightPercent: 70
resources:
  order: group
commands:
  - name: My Logs
    command: stern {{.Name}}
    type: selector
futureSection:
  enabled: true
`

// writeConfig writes data to a config.yaml in a fresh directory and returns
// its path.
func writeConfig(t *testing.T, data string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return data
}

// dirEntries lists the names in dir, to catch temporary files left behind.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func commandNames(cfg *appconfig.Config) []string {
	var names []string
	for _, c := range cfg.Commands {
		names = append(names, c.Name)
	}
	return names
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	t.Parallel()
	s := appconfig.NewStore(filepath.Join(t.TempDir(), "config.yaml"))
	cfg, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.LoadErr() != nil {
		t.Fatalf("LoadErr = %v, want nil", s.LoadErr())
	}
	if got, want := cfg.Viewer.Theme, appconfig.Default().Viewer.Theme; got != want {
		t.Fatalf("theme = %q, want %q", got, want)
	}
}

// TestLoadReportsBrokenFiles covers files of which nothing loads; files
// with a section that does not load are in load_sections_test.go.
func TestLoadReportsBrokenFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
	}{
		{name: "syntax error", data: "viewer:\n  theme: monokai\n commands: [\n"},
		{name: "list at the top", data: "- viewer\n- panel\n"},
		{name: "text at the top", data: "viewer\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := writeConfig(t, tc.data, 0o644)
			s := appconfig.NewStore(p)
			cfg, err := s.Load()
			var le *appconfig.LoadError
			if !errors.As(err, &le) {
				t.Fatalf("Load error = %v, want *LoadError", err)
			}
			if le.Path != p || !strings.Contains(err.Error(), p) {
				t.Fatalf("error %q does not name %s", err, p)
			}
			if !errors.Is(s.LoadErr(), le.Err) {
				t.Fatalf("LoadErr = %v, want %v", s.LoadErr(), err)
			}
			if len(le.Sections) != 0 || !le.ReadOnlyLost() {
				t.Fatalf("sections = %v, readOnly lost = %v; want none and true", le.Sections, le.ReadOnlyLost())
			}
			// Nothing of a file that does not parse is used, not even the
			// parts that would, and every context is read-only.
			if cfg.Viewer.Theme != appconfig.Default().Viewer.Theme {
				t.Fatalf("theme = %q, want the default", cfg.Viewer.Theme)
			}
			if !cfg.ReadOnly.For("any-context") {
				t.Fatalf("readOnly = %+v, want every context read-only", cfg.ReadOnly)
			}
		})
	}
}

func TestLoadReportsUnreadableFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file modes do not deny reads here")
	}
	p := writeConfig(t, customConfig, 0o000)
	cfg, err := appconfig.NewStore(p).Load()
	var le *appconfig.LoadError
	if !errors.As(err, &le) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Load error = %v, want a *LoadError for a permission error", err)
	}
	if !cfg.ReadOnly.For("any-context") {
		t.Fatalf("readOnly = %+v, want every context read-only", cfg.ReadOnly)
	}
}

// TestLoadWithoutPathFailsClosed is a store whose file kc cannot locate:
// it cannot tell what the file asks for, so every context is read-only.
func TestLoadWithoutPathFailsClosed(t *testing.T) {
	t.Parallel()
	cfg, err := appconfig.NewStore("").Load()
	if _, ok := errors.AsType[*appconfig.LoadError](err); !ok {
		t.Fatalf("Load error = %v, want a *LoadError", err)
	}
	if !cfg.ReadOnly.For("any-context") {
		t.Fatalf("readOnly = %+v, want every context read-only", cfg.ReadOnly)
	}
}

func TestLoadUsesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".kc"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".kc", "config.yaml")
	if err := os.WriteFile(p, []byte("viewer: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := appconfig.Load()
	var le *appconfig.LoadError
	if !errors.As(err, &le) || le.Path != p {
		t.Fatalf("Load error = %v, want a *LoadError for %s", err, p)
	}
	if got, gerr := appconfig.DefaultPath(); gerr != nil || got != p {
		t.Fatalf("DefaultPath = %q, %v; want %q", got, gerr, p)
	}
}

// TestUpdateRefusesBrokenFile covers the data loss in issue 59: a file that
// fails to load followed by "Save as defaults" must keep its bytes. A file
// of which only one section did not load is refused too: writing kc's
// defaults for that section would replace what the user wrote there.
func TestUpdateRefusesBrokenFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		original string
	}{
		{name: "syntax error", original: customConfig + "  broken: [\n"},
		{name: "one section does not load", original: customConfig + "kubernetes:\n  clusters:\n    ttl: 120\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := writeConfig(t, tc.original, 0o600)
			s := appconfig.NewStore(p)
			if _, err := s.Load(); err == nil {
				t.Fatal("Load succeeded on a broken file")
			}
			_, err := s.Update(func(c *appconfig.Config) { c.Viewer.Theme = "nord" })
			var le *appconfig.LoadError
			if !errors.As(err, &le) || le.Path != p {
				t.Fatalf("Update error = %v, want a *LoadError for %s", err, p)
			}
			if got := readFile(t, p); !bytes.Equal(got, []byte(tc.original)) {
				t.Fatalf("file changed:\n%s", got)
			}
			if names := dirEntries(t, filepath.Dir(p)); len(names) != 1 {
				t.Fatalf("directory holds %v, want only config.yaml", names)
			}
		})
	}
}

// TestUpdateAfterRepair shows the recovery path: once the user repairs the
// file, the next save merges into the repaired file, not into the defaults
// the session ran with.
func TestUpdateAfterRepair(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, "viewer: [\n", 0o644)
	s := appconfig.NewStore(p)
	if _, err := s.Load(); err == nil {
		t.Fatal("Load succeeded on a broken file")
	}
	if err := os.WriteFile(p, []byte(customConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Update(func(c *appconfig.Config) { c.Objects.Order = appconfig.ObjectsOrderCreation })
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := commandNames(saved); len(got) != 1 || got[0] != "My Logs" {
		t.Fatalf("commands = %v, want the repaired file's", got)
	}
}

func TestUpdatePreservesUnrelatedSettings(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, customConfig, 0o600)
	s := appconfig.NewStore(p)
	if _, err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Update(func(c *appconfig.Config) { c.Objects.Columns = appconfig.ColumnsModeWide }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	cfg, err := appconfig.NewStore(p).Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Objects.Columns != appconfig.ColumnsModeWide {
		t.Fatalf("objects.columns = %q, want wide", cfg.Objects.Columns)
	}
	if cfg.Viewer.Theme != "monokai" || cfg.Viewer.Mode != appconfig.ViewerModeWrap {
		t.Fatalf("viewer = %+v, want monokai/wrap", cfg.Viewer)
	}
	if cfg.Panel.Width.LeftPercent != 30 || cfg.Resources.Order != appconfig.OrderGroup {
		t.Fatalf("panel width %+v, order %q changed", cfg.Panel.Width, cfg.Resources.Order)
	}
	if got := commandNames(cfg); len(got) != 1 || got[0] != "My Logs" || cfg.Commands[0].Command != "stern {{.Name}}" {
		t.Fatalf("commands = %+v, want the custom one", cfg.Commands)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(readFile(t, p), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["futureSection"]; !ok {
		t.Fatalf("unknown key futureSection was dropped:\n%s", readFile(t, p))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	if names := dirEntries(t, filepath.Dir(p)); len(names) != 1 {
		t.Fatalf("directory holds %v, want only config.yaml", names)
	}
}

func TestUpdateCreatesMissingFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), ".kc", "config.yaml")
	s := appconfig.NewStore(p)
	if _, err := s.Update(func(c *appconfig.Config) { c.Viewer.Theme = "nord" }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg, err := appconfig.NewStore(p).Load()
	if err != nil || cfg.Viewer.Theme != "nord" {
		t.Fatalf("reload = %q, %v; want nord", cfg.Viewer.Theme, err)
	}
}

func TestUpdateRewritesMixedCaseKeys(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, "Viewer:\n  Theme: monokai\n  Mode: wrap\n", 0o644)
	s := appconfig.NewStore(p)
	cfg, err := s.Load()
	if err != nil || cfg.Viewer.Theme != "monokai" {
		t.Fatalf("Load = %q, %v; want monokai", cfg.Viewer.Theme, err)
	}
	if _, err := s.Update(func(c *appconfig.Config) { c.Viewer.Mode = appconfig.ViewerModeScroll }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(readFile(t, p), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["Viewer"]; ok {
		t.Fatalf("old key spelling kept next to the new one:\n%s", readFile(t, p))
	}
	viewer, _ := raw["viewer"].(map[string]any)
	if viewer["theme"] != "monokai" || viewer["mode"] != appconfig.ViewerModeScroll {
		t.Fatalf("viewer = %v, want monokai/scroll", viewer)
	}
}

func TestUpdateWritesThroughSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "kc.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(customConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := appconfig.NewStore(link).Update(func(c *appconfig.Config) { c.Viewer.Theme = "nord" }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symbolic link was replaced by a file")
	}
	cfg, err := appconfig.NewStore(target).Load()
	if err != nil || cfg.Viewer.Theme != "nord" {
		t.Fatalf("target = %q, %v; want nord", cfg.Viewer.Theme, err)
	}
}

func TestUpdateReportsWriteFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory modes do not deny writes here")
	}
	p := writeConfig(t, customConfig, 0o644)
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := appconfig.NewStore(p).Update(func(c *appconfig.Config) { c.Viewer.Theme = "nord" })
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("Update error = %v, want a write error naming %s", err, p)
	}
	if got := readFile(t, p); !bytes.Equal(got, []byte(customConfig)) {
		t.Fatalf("file changed:\n%s", got)
	}
}

// TestConcurrentUpdatesKeepEveryChange guards the read, change and write
// sequence: each Update reads what the one before it wrote.
func TestConcurrentUpdatesKeepEveryChange(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, customConfig, 0o644)
	s := appconfig.NewStore(p)
	changes := []func(*appconfig.Config){
		func(c *appconfig.Config) { c.Viewer.Theme = "nord" },
		func(c *appconfig.Config) { c.Objects.Columns = appconfig.ColumnsModeWide },
		func(c *appconfig.Config) { c.Panel.Width.LeftPercent = 40 },
		func(c *appconfig.Config) { c.Resources.ShowNonEmptyOnly = false },
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(changes))
	for _, change := range changes {
		wg.Go(func() {
			if _, err := s.Update(change); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Update: %v", err)
	}
	cfg, err := appconfig.NewStore(p).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Viewer.Theme != "nord" || cfg.Objects.Columns != appconfig.ColumnsModeWide ||
		cfg.Panel.Width.LeftPercent != 40 || cfg.Resources.ShowNonEmptyOnly {
		t.Fatalf("a change was lost: %+v", cfg)
	}
}
