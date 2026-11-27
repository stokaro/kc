package kubeconfig_test

import (
	"path/filepath"
	"testing"

	"github.com/stokaro/kc/pkg/kubeconfig"
)

func TestCurrentContext(t *testing.T) {
	tests := []struct {
		name  string
		home  bool // files under ~/.kube, else $KUBECONFIG in this order
		files []kubeFile
		// wantName is the current-context, wantFile the file that names
		// it and wantFrom the file of the context it selects ("" for none).
		wantName, wantFile, wantFrom string
	}{
		{
			name:     "defined",
			files:    []kubeFile{{name: "a", current: "good", contexts: []string{"bad", "good"}}},
			wantName: "good", wantFile: "a", wantFrom: "a",
		},
		{
			name:     "not defined",
			files:    []kubeFile{{name: "a", current: "typo-context", contexts: []string{"badtoken", "good", "limited", "refused"}}},
			wantName: "typo-context", wantFile: "a",
		},
		{
			name:  "none named",
			files: []kubeFile{{name: "a", contexts: []string{"one", "two"}}},
		},
		{
			name: "list: the first file that names one decides",
			files: []kubeFile{
				{name: "a", current: "typo", contexts: []string{"a1"}},
				{name: "b", current: "b1", contexts: []string{"b1"}},
			},
			wantName: "typo", wantFile: "a",
		},
		{
			name: "list: a later file when the first names none",
			files: []kubeFile{
				{name: "a", contexts: []string{"a1"}},
				{name: "b", current: "b1", contexts: []string{"b1"}},
			},
			wantName: "b1", wantFile: "b", wantFrom: "b",
		},
		{
			name: "list: a context of another file of the list",
			files: []kubeFile{
				{name: "a", current: "b1", contexts: []string{"a1"}},
				{name: "b", contexts: []string{"b1"}},
			},
			wantName: "b1", wantFile: "a", wantFrom: "b",
		},
		{
			name: "list: a name two files define comes from the first",
			files: []kubeFile{
				{name: "a", contexts: []string{"shared"}},
				{name: "b", current: "shared", contexts: []string{"shared"}},
			},
			wantName: "shared", wantFile: "b", wantFrom: "a",
		},
		{
			name: "home: ~/.kube/config first",
			home: true,
			files: []kubeFile{
				{name: "a.yaml", current: "a1", contexts: []string{"a1"}},
				{name: "config", current: "main", contexts: []string{"main"}},
			},
			wantName: "main", wantFile: "config", wantFrom: "config",
		},
		{
			name: "home: the first other file when config names none",
			home: true,
			files: []kubeFile{
				{name: "config", contexts: []string{"main"}},
				{name: "b.yaml", current: "b1", contexts: []string{"b1"}},
				{name: "a.yaml", current: "a1", contexts: []string{"a1"}},
			},
			wantName: "a1", wantFile: "a.yaml", wantFrom: "a.yaml",
		},
		{
			name: "home: not defined in config, another file's is not used",
			home: true,
			files: []kubeFile{
				{name: "config", current: "typo", contexts: []string{"main"}},
				{name: "a.yaml", current: "a1", contexts: []string{"a1"}},
			},
			wantName: "typo", wantFile: "config",
		},
		{
			name: "home: files stand alone",
			home: true,
			files: []kubeFile{
				{name: "config", current: "elsewhere", contexts: []string{"main"}},
				{name: "a.yaml", contexts: []string{"elsewhere"}},
			},
			wantName: "elsewhere", wantFile: "config",
		},
		{
			name: "home: the file's own entry, not an earlier file's",
			home: true,
			files: []kubeFile{
				{name: "a.yaml", contexts: []string{"kind"}},
				{name: "b.yaml", current: "kind", contexts: []string{"kind"}},
			},
			wantName: "kind", wantFile: "b.yaml", wantFrom: "b.yaml",
		},
		{
			name:  "home: none named",
			home:  true,
			files: []kubeFile{{name: "config", contexts: []string{"one"}}, {name: "a.yaml", contexts: []string{"two"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dir, list string
			if tc.home {
				dir = setupHome(t, tc.files)
			} else {
				dir, list = setupEnv(t, tc.files)
			}
			m := discover(t)
			path := func(name string) string {
				if name == "" {
					return ""
				}
				return filepath.Join(dir, name)
			}
			got := m.CurrentContext()
			if got.Name != tc.wantName || got.File != path(tc.wantFile) {
				t.Fatalf("CurrentContext() = %q in %q, want %q in %q", got.Name, got.File, tc.wantName, path(tc.wantFile))
			}
			switch {
			case tc.wantFrom == "" && got.Context != nil:
				t.Fatalf("CurrentContext().Context = %s of %s, want none", got.Context.Name, got.Context.Kubeconfig.Path)
			case tc.wantFrom != "" && got.Context == nil:
				t.Fatalf("CurrentContext().Context = nil, want %s of %s", tc.wantName, path(tc.wantFrom))
			case tc.wantFrom != "" && (got.Context.Name != tc.wantName || got.Context.Kubeconfig.Path != path(tc.wantFrom)):
				t.Fatalf("CurrentContext().Context = %s of %s, want %s of %s", got.Context.Name, got.Context.Kubeconfig.Path, tc.wantName, path(tc.wantFrom))
			}
			if tc.home {
				return
			}
			// For $KUBECONFIG, the name is the one kubectl reads from
			// the merged files.
			merged, err := kubeconfig.LoadingRules(list).Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != merged.CurrentContext {
				t.Fatalf("CurrentContext().Name = %q, clientcmd merges %q", got.Name, merged.CurrentContext)
			}
		})
	}
}
