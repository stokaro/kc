package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/models"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type testLink struct {
	*models.RowItem
	ref     models.ObjectRef
	missing string
}

func (l testLink) LinkTarget() (models.ObjectRef, string) { return l.ref, l.missing }

// runLinkCmds runs the commands a link returns, feeding retry messages back
// until none is left.
func runLinkCmds(t *testing.T, app *App, cmd tea.Cmd) []string {
	t.Helper()
	var toasts []string
	queue := []tea.Cmd{cmd}
	deadline := time.Now().Add(10 * time.Second)
	for len(queue) > 0 && time.Now().Before(deadline) {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case linkSelectMsg:
			queue = append(queue, app.selectLinkedRow(msg))
		case showToastMsg:
			toasts = append(toasts, msg.text)
		}
	}
	return toasts
}

func TestOpenLinkedObjectInOtherPanelEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	const ns = "link-target"
	t.Cleanup(createNamespace(t, cl, ns))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web-config"}}
	if err := cl.GetClient().Create(ctx, cm); err != nil {
		t.Fatalf("create configmap: %v", err)
	}
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace("default")
	app.activePanel = 0

	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	cases := []struct {
		name      string
		ref       models.ObjectRef
		missing   string
		wantPath  string
		wantScope string
		wantSel   string
		wantToast bool
	}{
		{name: "existing object", ref: models.ObjectRef{GVR: gvr, Namespaced: true, Namespace: ns, Name: "web-config"},
			wantPath: "/configmaps", wantScope: ns, wantSel: "web-config"},
		{name: "missing object still opens its list", ref: models.ObjectRef{GVR: gvr, Namespaced: true, Namespace: ns, Name: "gone"},
			missing: "ConfigMap gone is not in the cluster", wantPath: "/configmaps", wantScope: ns, wantToast: true},
		{name: "type not served moves nothing", missing: "example.com/v1 Widget is not served",
			wantPath: "/configmaps", wantScope: ns, wantToast: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			link := testLink{RowItem: models.NewRowItem("x", []string{"@x"}, nil, nil), ref: tc.ref, missing: tc.missing}
			toasts := runLinkCmds(t, app, app.openLinkedObject(link))
			if got := app.rightPanel.GetCurrentPath(); got != tc.wantPath {
				t.Errorf("right panel path = %q, want %q", got, tc.wantPath)
			}
			if app.panelScope[1] != tc.wantScope {
				t.Errorf("right panel scope = %q, want %q", app.panelScope[1], tc.wantScope)
			}
			if app.activePanel != 0 {
				t.Errorf("focus moved to panel %d", app.activePanel)
			}
			if tc.wantSel != "" {
				sctx, scancel := context.WithTimeout(t.Context(), time.Second)
				sel := app.rightPanel.currentSelectionID(sctx)
				scancel()
				if sel != tc.wantSel {
					t.Errorf("selection = %q, want %q", sel, tc.wantSel)
				}
			}
			if (len(toasts) > 0) != tc.wantToast || (tc.wantToast && !strings.Contains(strings.Join(toasts, "|"), tc.missing)) {
				t.Errorf("messages = %q, want toast %v with %q", toasts, tc.wantToast, tc.missing)
			}
		})
	}
}
