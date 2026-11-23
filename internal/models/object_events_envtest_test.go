package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

var configMapsGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// TestObjectEventsMatchTheSubjectUID lists a ConfigMap's /events: only the
// Events about the ConfigMap that exists now, not those of an earlier one
// of the same name or of another object, newest Last Seen first.
func TestObjectEventsMatchTheSubjectUID(t *testing.T) {
	t.Parallel()
	kcenvtest.Require(t, testCfg)
	ctx := t.Context()
	cl := startTestCluster(t, testCfg)
	cli := cl.GetClient()
	ns := kctesting.UniqueName("object-events")
	createNamespace(t, cl, ns)

	old := &corev1.ConfigMap{Namespace: ns, Name: "cfg"}
	mustCreate(t, cli, old)
	if err := cli.Delete(ctx, old); err != nil {
		t.Fatalf("delete the first cfg: %v", err)
	}
	current := &corev1.ConfigMap{Namespace: ns, Name: "cfg"}
	mustCreate(t, cli, current)
	other := &corev1.ConfigMap{Namespace: ns, Name: "other"}
	mustCreate(t, cli, other)
	if current.UID == old.UID || current.UID == "" {
		t.Fatalf("the replacement has UID %q, the first %q", current.UID, old.UID)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	event := func(name string, cm *corev1.ConfigMap, typ string, last time.Duration, msg string) {
		mustCreate(t, cli, &corev1.Event{
			Namespace: ns, Name: name,
			InvolvedObject: corev1.ObjectReference{Kind: "ConfigMap", APIVersion: "v1", Namespace: ns, Name: cm.Name, UID: cm.UID},
			Type:           typ, Reason: "Test", Message: msg,
			FirstTimestamp: metav1.NewTime(base), LastTimestamp: metav1.NewTime(base.Add(last)), Count: 3,
		})
	}
	event("cfg.old", old, corev1.EventTypeWarning, 50*time.Minute, "about the deleted cfg")
	event("cfg.warning", current, corev1.EventTypeWarning, 10*time.Minute, strings.Repeat("a long message ", 20))
	event("cfg.normal", current, corev1.EventTypeNormal, 40*time.Minute, "newest")
	event("other.normal", other, corev1.EventTypeNormal, 55*time.Minute, "another object")

	deps := Deps{Cl: cl, Ctx: ctx, CtxName: "envtest", AppConfig: appconfig.Default()}
	folder := NewObjectEventsFolder(deps, configMapsGVR, ns, "cfg", []string{"namespaces", ns, "configmaps", "cfg", EventsSegment})
	t.Cleanup(folder.Close)

	ids := func() []string {
		var out []string
		for _, r := range folder.Lines(ctx, 0, folder.Len(ctx)) {
			if id, _, _, ok := r.Columns(); ok && id != "__back__" {
				out = append(out, id)
			}
		}
		return out
	}
	want := []string{"cfg.normal", "cfg.warning"}
	deadline := time.Now().Add(15 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		if got = ids(); slices.Equal(got, want) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("/events of cfg lists %v, want %v (the current cfg's Events, newest first)", got, want)
	}

	cmd := folder.KubectlCommand()
	if !strings.Contains(cmd, "get events -n "+ns+" --field-selector involvedObject.name=cfg") || !strings.Contains(cmd, "kc also matches its UID") {
		t.Fatalf("kubectl command %q", cmd)
	}
}

// TestObjectEventsRow checks where /events is offered: inside namespaced
// objects, not inside cluster-scoped ones or Events.
func TestObjectEventsRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		gvr       schema.GroupVersionResource
		namespace string
		want      bool
	}{
		{"configmap", configMapsGVR, "web", true},
		{"deployment", relDeploymentsGVR, "web", true},
		{"cluster scoped", schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "", false},
		{"core event", ovEventsGVR, "web", false},
		{"events.k8s.io event", schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}, "web", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := false
			if fn, ok := builtinObjectRows(tc.gvr); ok {
				for _, r := range fn(Deps{}, tc.namespace, "x", []string{"x"}) {
					if id, _, _, ok := r.Columns(); ok && strings.HasSuffix(id, "/"+EventsSegment) {
						got = true
					}
				}
			}
			if got != tc.want {
				t.Fatalf("/events offered = %v, want %v", got, tc.want)
			}
		})
	}
}
