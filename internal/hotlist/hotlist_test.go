package hotlist_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/sttts/kc/internal/hotlist"
	"github.com/sttts/kc/internal/navigation"
)

var (
	pods = navigation.Location{Context: "mba", Namespace: "default", Steps: []string{"default//v1/pods"}, Path: "/pods"}
	web  = navigation.Location{Context: "mba", Namespace: "default", Steps: []string{"default//v1/pods", "web"}, Path: "/pods/web"}
	prod = navigation.Location{Context: "prod", Namespace: "default", Steps: []string{"default//v1/pods"}, Path: "/pods"}
)

func paths(entries []navigation.Location) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Context+":"+e.Path)
	}
	return out
}

func TestAdd(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		start     []navigation.Location
		add       navigation.Location
		wantAdded bool
		want      []string
	}{
		{"to empty", nil, pods, true, []string{"mba:/pods"}},
		{"appends", []navigation.Location{pods}, web, true, []string{"mba:/pods", "mba:/pods/web"}},
		{"skips a duplicate", []navigation.Location{pods, web}, pods, false, []string{"mba:/pods", "mba:/pods/web"}},
		{"same path in another context is new", []navigation.Location{pods}, prod, true, []string{"mba:/pods", "prod:/pods"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := slices.Clone(tc.start)
			got, added := hotlist.Add(start, tc.add)
			if added != tc.wantAdded {
				t.Fatalf("added = %v, want %v", added, tc.wantAdded)
			}
			if !slices.Equal(paths(got), tc.want) {
				t.Fatalf("entries = %v, want %v", paths(got), tc.want)
			}
			if !slices.Equal(paths(start), paths(tc.start)) {
				t.Fatalf("Add changed its input: %v", paths(start))
			}
		})
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		index int
		want  []string
	}{
		{"first", 0, []string{"mba:/pods/web", "prod:/pods"}},
		{"last", 2, []string{"mba:/pods", "mba:/pods/web"}},
		{"out of range", 3, []string{"mba:/pods", "mba:/pods/web", "prod:/pods"}},
		{"negative", -1, []string{"mba:/pods", "mba:/pods/web", "prod:/pods"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := []navigation.Location{pods, web, prod}
			got := hotlist.Remove(start, tc.index)
			if !slices.Equal(paths(got), tc.want) {
				t.Fatalf("entries = %v, want %v", paths(got), tc.want)
			}
			if len(start) != 3 || start[0].Path != "/pods" {
				t.Fatalf("Remove changed its input: %v", paths(start))
			}
		})
	}
}

func TestSaveLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []navigation.Location
	}{
		{"empty", nil},
		{"one", []navigation.Location{pods}},
		{"several", []navigation.Location{pods, web, prod, {Context: "mba", Namespace: "", Path: "/"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "kc", hotlist.FileName)
			if err := hotlist.Save(path, tc.entries); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := hotlist.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !slices.EqualFunc(got, tc.entries, func(a, b navigation.Location) bool { return a.Equal(b) && a.Path == b.Path }) {
				t.Fatalf("loaded %v, want %v", got, tc.entries)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat: %v", err)
				}
				if perm := info.Mode().Perm(); perm != 0o600 {
					t.Fatalf("file mode = %o, want 600", perm)
				}
			}
			left, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatalf("read dir: %v", err)
			}
			if len(left) != 1 {
				t.Fatalf("directory holds %d files, want only the hotlist", len(left))
			}
		})
	}
}

func TestSaveReplaces(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), hotlist.FileName)
	if err := hotlist.Save(path, []navigation.Location{pods, web}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := hotlist.Save(path, []navigation.Location{prod}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := hotlist.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(paths(got), []string{"prod:/pods"}) {
		t.Fatalf("entries = %v, want [prod:/pods]", paths(got))
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content *string
		want    []string
		wantErr bool
	}{
		{name: "missing file is empty", content: nil},
		{name: "empty file", content: new("")},
		{name: "hand written", content: new("entries:\n- context: mba\n  namespace: default\n  path: /pods\n  steps: [\"default//v1/pods\"]\n"), want: []string{"mba:/pods"}},
		{name: "broken yaml", content: new("entries: [:"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), hotlist.FileName)
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			got, err := hotlist.Load(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !slices.Equal(paths(got), tc.want) {
				t.Fatalf("entries = %v, want %v", paths(got), tc.want)
			}
		})
	}
}
