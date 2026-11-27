package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
)

var gvrConfigMaps = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

func objectRow(uid types.UID, origin models.ObjectOrigin) *models.ObjectRow {
	row := models.NewObjectRow("demo", []string{"demo"}, nil, gvrConfigMaps, "team", "demo", nil)
	row.SetInstance(uid, origin)
	return row
}

func TestDeleteTargetFor(t *testing.T) {
	t.Parallel()
	cl := &kccluster.Cluster{}
	origin := models.ObjectOrigin{Cluster: cl, Context: "kind-dev"}

	refused := []struct {
		name string
		item models.Item
	}{
		{name: "item without instance identity", item: stubObject{id: "demo", namespace: "team", name: "demo"}},
		{name: "row without UID", item: objectRow("", origin)},
		{name: "row without cluster", item: objectRow("uid-1", models.ObjectOrigin{Context: "kind-dev"})},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := deleteTargetFor(0, tc.item); !errors.Is(err, errNoInstance) {
				t.Fatalf("deleteTargetFor() error = %v, want errNoInstance", err)
			}
		})
	}

	t.Run("row with UID and cluster", func(t *testing.T) {
		t.Parallel()
		got, err := deleteTargetFor(1, objectRow("uid-1", origin))
		if err != nil {
			t.Fatalf("deleteTargetFor() error = %v", err)
		}
		want := deleteTarget{panelIdx: 1, gvr: gvrConfigMaps, namespace: "team", name: "demo", uid: "uid-1", cluster: cl, context: "kind-dev",
			noun: "configmaps", label: "configmaps team/demo"}
		if got != want {
			t.Fatalf("deleteTargetFor() = %+v, want %+v", got, want)
		}
	})

	t.Run("row that knows its kind", func(t *testing.T) {
		t.Parallel()
		row := objectRow("uid-1", origin)
		row.SetKind("ConfigMap")
		got, err := deleteTargetFor(1, row)
		if err != nil {
			t.Fatalf("deleteTargetFor() error = %v", err)
		}
		if got.label != "configmap team/demo" {
			t.Fatalf("label = %q, want %q", got.label, "configmap team/demo")
		}
	})
}

// The fake client checks only ResourceVersion preconditions, so these tests
// record the options it receives; the envtest regression proves what the
// API server does with them.
func TestDeleteObjectInstanceSendsUIDPrecondition(t *testing.T) {
	t.Parallel()
	var calls int
	var gotOpts crclient.DeleteOptions
	var gotKey crclient.ObjectKey
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(_ context.Context, _ crclient.WithWatch, obj crclient.Object, opts ...crclient.DeleteOption) error {
			calls++
			gotKey = crclient.ObjectKeyFromObject(obj)
			gotOpts.ApplyOptions(opts)
			return nil
		},
	}).Build()
	target := deleteTarget{gvr: gvrConfigMaps, namespace: "team", name: "demo", uid: "uid-1"}

	if err := deleteObjectInstance(t.Context(), c, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, target); err != nil {
		t.Fatalf("deleteObjectInstance() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("Delete calls = %d, want 1", calls)
	}
	if gotKey != (crclient.ObjectKey{Namespace: "team", Name: "demo"}) {
		t.Fatalf("Delete key = %v, want team/demo", gotKey)
	}
	if gotOpts.Preconditions == nil || gotOpts.Preconditions.UID == nil {
		t.Fatalf("Delete preconditions = %+v, want a UID precondition", gotOpts.Preconditions)
	}
	if got := *gotOpts.Preconditions.UID; got != "uid-1" {
		t.Fatalf("precondition UID = %q, want uid-1", got)
	}
	if gotOpts.Preconditions.ResourceVersion != nil {
		t.Fatalf("precondition ResourceVersion = %q, want none", *gotOpts.Preconditions.ResourceVersion)
	}
}

func TestDeleteObjectInstanceRefusesEmptyUID(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(context.Context, crclient.WithWatch, crclient.Object, ...crclient.DeleteOption) error {
			t.Error("Delete called for a target without UID")
			return nil
		},
	}).Build()
	target := deleteTarget{gvr: gvrConfigMaps, namespace: "team", name: "demo"}
	err := deleteObjectInstance(t.Context(), c, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, target)
	if !errors.Is(err, errNoInstance) {
		t.Fatalf("deleteObjectInstance() error = %v, want errNoInstance", err)
	}
}

func TestExecuteDeleteWithoutClusterDeletesNothing(t *testing.T) {
	t.Parallel()
	msg := executeDelete(t.Context(), deleteTarget{gvr: gvrConfigMaps, name: "demo", uid: "uid-1"})
	if !errors.Is(msg.err, errNoInstance) {
		t.Fatalf("executeDelete() error = %v, want errNoInstance", msg.err)
	}
}

func TestDeleteResultText(t *testing.T) {
	t.Parallel()
	gr := gvrConfigMaps.GroupResource()
	target := deleteTarget{gvr: gvrConfigMaps, namespace: "team", name: "demo", label: "configmap team/demo"}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "replaced", err: apierrors.NewConflict(gr, "demo", errors.New("Precondition failed: UID in precondition: a, UID in object meta: b")), want: "configmap team/demo was replaced by a new object with the same name; nothing was deleted"},
		{name: "already gone", err: apierrors.NewNotFound(gr, "demo"), want: "configmap team/demo is already gone; nothing was deleted"},
		{name: "other failure", err: apierrors.NewForbidden(gr, "demo", errors.New("no")), want: "Delete configmap team/demo failed: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deleteResultText(resourceDeletedMsg{target: target, err: tc.err})
			if !strings.Contains(got, tc.want) {
				t.Fatalf("deleteResultText() = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestDeleteConfirmShowsContext(t *testing.T) {
	t.Parallel()
	model := NewDeleteConfirmModel()
	model.SetDimensions(60, 8)

	model.Configure("configmap team/demo", "kind-dev")
	withContext := model.View().Content
	if !strings.Contains(withContext, "Delete configmap team/demo?") {
		t.Fatalf("view does not name the object as kubectl does:\n%s", withContext)
	}
	if !strings.Contains(withContext, "Context: kind-dev") {
		t.Fatalf("view does not show the context:\n%s", withContext)
	}
	// The buttons move down one line; clicks must follow them.
	if got := model.buttonRect[0].y; got != 3 {
		t.Fatalf("Yes button line = %d, want 3", got)
	}

	model.Configure("configmap team/demo", "")
	if strings.Contains(model.View().Content, "Context:") {
		t.Fatalf("view shows a context line without a context")
	}
	if got := model.buttonRect[0].y; got != 2 {
		t.Fatalf("Yes button line without context = %d, want 2", got)
	}
}

func TestObjectLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		gvr       schema.GroupVersionResource
		kind      string
		namespace string
		want      string
	}{
		{name: "core kind", gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, kind: "Pod", namespace: "shop", want: "pod shop/web"},
		{name: "grouped kind", gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, kind: "Deployment", namespace: "shop", want: "deployment.apps shop/web"},
		{name: "cluster-scoped", gvr: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, kind: "Node", want: "node web"},
		{name: "kind unknown", gvr: schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}, namespace: "shop", want: "widgets.example.com shop/web"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := objectLabel(tc.gvr, tc.kind, tc.namespace, "web"); got != tc.want {
				t.Fatalf("objectLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}
