package models

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stokaro/kc/pkg/appconfig"
)

func nodeShellDeps(enabled bool, contexts ...string) Deps {
	cfg := appconfig.Default()
	cfg.NodeShell.Enabled = enabled
	cfg.NodeShell.Contexts = contexts
	cfg.NodeShell.Namespace = "ops"
	cfg.NodeShell.Image = "busybox:1"
	cfg.NodeShell.MaxDuration = metav1.Duration{Duration: 2 * time.Hour}
	return Deps{AppConfig: cfg, CtxName: "mba", KubectlContext: "other"}
}

func TestNodeChildFolderOffersShellOnlyWhenEnabled(t *testing.T) {
	tests := []struct {
		name string
		deps Deps
		want bool
	}{
		{name: "no config", deps: Deps{}, want: false},
		{name: "disabled", deps: nodeShellDeps(false), want: false},
		{name: "enabled everywhere", deps: nodeShellDeps(true), want: true},
		{name: "enabled for this context", deps: nodeShellDeps(true, "kind", "mba"), want: true},
		{name: "enabled for another context", deps: nodeShellDeps(true, "kind"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder := NewNodeChildFolder(tt.deps, "node-a", []string{"nodes", "node-a"})
			rows, err := folder.populate(t.Context())
			if err != nil {
				t.Fatalf("populate: %v", err)
			}
			var shell *NodeShellItem
			for _, r := range rows {
				if s, ok := r.(*NodeShellItem); ok {
					shell = s
				}
			}
			if got := shell != nil; got != tt.want {
				t.Fatalf("shell row offered = %v, want %v (rows %d)", got, tt.want, len(rows))
			}
			if _, cells, _, _ := rows[0].Columns(); cells[0] != "/pods" {
				t.Errorf("first row = %v, want /pods", cells)
			}
		})
	}
}

func TestNodeShellConfirmationNamesWhatIsCreated(t *testing.T) {
	item := &NodeShellItem{SimpleItem: NewSimpleItem("x", []string{"x"}, nil, nil), deps: nodeShellDeps(true), node: "node-a"}
	c := item.Confirmation()

	details := map[string]string{}
	for _, d := range c.Details {
		details[d[0]] = d[1]
	}
	if details["Context"] != "mba" || details["Node"] != "node-a" || details["Namespace"] != "ops" || details["Image"] != "busybox:1" {
		t.Errorf("details = %v", details)
	}
	if !strings.HasPrefix(details["Pod"], "kc-node-shell-node-a-") {
		t.Errorf("pod = %q, want kc-node-shell-node-a-<suffix>", details["Pod"])
	}
	notes := strings.Join(c.Notes, " ")
	for _, want := range []string{"PRIVILEGED", "host PID, network and IPC", "tolerating every taint", "deletes the pod", "2h0m0s"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, strings.Join(c.Notes, "\n"))
		}
	}
	// The kubectl line names the context, since plain kubectl uses another.
	if want := "kubectl --context mba debug node/node-a -n ops -it --profile=sysadmin --image=busybox:1 -- nsenter -t 1 -m -u -i -n -p -- sh"; !strings.Contains(strings.Join(strings.Fields(notes), " "), want) {
		t.Errorf("notes lack the kubectl command %q:\n%s", want, strings.Join(c.Notes, "\n"))
	}
	for _, line := range c.Notes {
		if len(line) > 52 {
			t.Errorf("note line too wide for the dialog (%d): %q", len(line), line)
		}
	}

	req, ok := item.FollowUp().(NodeShellRequest)
	if !ok {
		t.Fatalf("follow-up = %T, want NodeShellRequest", item.FollowUp())
	}
	if req.Pod != details["Pod"] || req.Node != "node-a" || req.Namespace != "ops" || req.Context != "mba" {
		t.Errorf("request = %+v, want the pod the dialog named (%s)", req, details["Pod"])
	}
}

func TestWrapWords(t *testing.T) {
	got := wrapWords("aaa bbb ccc dddddddddd", 8)
	want := []string{"aaa bbb", "  ccc", "  dddddddddd"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapWords = %q, want %q", got, want)
	}
}
