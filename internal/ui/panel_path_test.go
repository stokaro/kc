package ui

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sttts/kc/internal/models"
)

func names(ss ...string) []PathElem {
	elems := make([]PathElem, 0, len(ss))
	for _, s := range ss {
		elems = append(elems, PathElem{Name: s})
	}
	return elems
}

func TestParsePanelPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    PanelPath
		wantErr bool
	}{
		{in: "/", want: PanelPath{Anchor: AnchorRoot}},
		{in: "/namespaces/default/pods", want: PanelPath{Anchor: AnchorRoot, Elems: names("namespaces", "default", "pods")}},
		{in: "//pods//nginx/", want: PanelPath{Anchor: AnchorRoot, Elems: names("pods", "nginx")}},
		{in: "pods", want: PanelPath{Anchor: AnchorCurrent, Elems: names("pods")}},
		{in: "deploy/myapp", want: PanelPath{Anchor: AnchorCurrent, Elems: names("deploy", "myapp")}},
		{in: "ns/default", want: PanelPath{Anchor: AnchorCurrent, Elems: names("ns", "default")}},
		{in: "..", want: PanelPath{Anchor: AnchorCurrent, Elems: names("..")}},
		{in: "../svc", want: PanelPath{Anchor: AnchorCurrent, Elems: names("..", "svc")}},
		{in: ".", want: PanelPath{Anchor: AnchorCurrentStrict}},
		{in: "./pods", want: PanelPath{Anchor: AnchorCurrentStrict, Elems: names("pods")}},
		{in: "a/./b", want: PanelPath{Anchor: AnchorCurrent, Elems: names("a", "b")}},
		{in: "~", want: PanelPath{Anchor: AnchorHome}},
		{in: "~/pods", want: PanelPath{Anchor: AnchorHome, Elems: names("pods")}},
		{in: "~bob", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePanelPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePanelPath(%q) = %+v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePanelPath(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParsePanelPath(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseCdLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		line    string
		want    cdRequest
		notCd   bool
		wantErr bool
	}{
		{name: "bare cd goes home", line: "cd", want: cdRequest{path: PanelPath{Anchor: AnchorHome}}},
		{name: "spaces around", line: "  cd   pods  ", want: cdRequest{path: PanelPath{Anchor: AnchorCurrent, Elems: names("pods")}}},
		{name: "absolute", line: "cd /namespaces/x", want: cdRequest{path: PanelPath{Anchor: AnchorRoot, Elems: names("namespaces", "x")}}},
		{name: "previous", line: "cd -", want: cdRequest{previous: true}},
		{name: "namespace flag", line: "cd -n kube-system svc", want: cdRequest{path: PanelPath{Anchor: AnchorCurrent, Rescope: true, Namespace: "kube-system", Elems: names("svc")}}},
		{name: "namespace after path", line: "cd svc -n kube-system", want: cdRequest{path: PanelPath{Anchor: AnchorCurrent, Rescope: true, Namespace: "kube-system", Elems: names("svc")}}},
		{name: "long namespace flag", line: "cd --namespace=x po", want: cdRequest{path: PanelPath{Anchor: AnchorCurrent, Rescope: true, Namespace: "x", Elems: names("po")}}},
		{name: "namespace only", line: "cd -n x", want: cdRequest{path: PanelPath{Anchor: AnchorRoot, Rescope: true, Namespace: "x"}}},
		{name: "all namespaces", line: "cd -A pods", want: cdRequest{path: PanelPath{Anchor: AnchorCurrent, Rescope: true, Namespace: models.AllNamespaces, Elems: names("pods")}}},
		{name: "home with path", line: "cd ~/pods", want: cdRequest{path: PanelPath{Anchor: AnchorHome, Elems: names("pods")}}},

		{name: "other command", line: "ls -l", notCd: true},
		{name: "cd prefix only", line: "cdx pods", notCd: true},
		{name: "builtin reaches the shell", line: "builtin cd /tmp", notCd: true},
		{name: "empty", line: "   ", notCd: true},
		{name: "command list", line: "cd /tmp && ls", notCd: true},
		{name: "variable", line: "cd $HOME", notCd: true},
		{name: "quoted", line: `cd "my dir"`, notCd: true},
		{name: "glob", line: "cd foo*", notCd: true},

		{name: "missing namespace", line: "cd -n", wantErr: true},
		{name: "unknown option", line: "cd -x pods", wantErr: true},
		{name: "two paths", line: "cd pods svc", wantErr: true},
		{name: "previous with path", line: "cd - pods", wantErr: true},
		{name: "bad home", line: "cd ~bob", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseCdLine(tc.line)
			switch {
			case tc.notCd:
				if !errors.Is(err, errNotCd) {
					t.Fatalf("parseCdLine(%q) = %+v, %v; want errNotCd", tc.line, got, err)
				}
				return
			case tc.wantErr:
				if err == nil || errors.Is(err, errNotCd) {
					t.Fatalf("parseCdLine(%q) = %+v, %v; want a usage error", tc.line, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCdLine(%q): %v", tc.line, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseCdLine(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestPanelPathString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		p    PanelPath
		want string
	}{
		{p: PanelPath{Anchor: AnchorRoot}, want: "/"},
		{p: PanelPath{Anchor: AnchorRoot, Rescope: true, Namespace: "x", Elems: []PathElem{{ID: "x//v1/pods"}, {Name: "nginx"}}}, want: "-n x /x//v1/pods/nginx"},
		{p: PanelPath{Anchor: AnchorCurrent, Rescope: true, Elems: names("pods")}, want: "-A pods"},
		{p: PanelPath{Anchor: AnchorHome}, want: "~"},
		{p: PanelPath{Anchor: AnchorCurrentStrict, Elems: names("a")}, want: "./a"},
		{p: PanelPath{Anchor: AnchorCurrent}, want: "."},
	}
	for _, tc := range tests {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.p, got, tc.want)
		}
	}
}
