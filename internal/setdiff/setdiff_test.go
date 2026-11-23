package setdiff_test

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/setdiff"
)

var cmGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// cm is a ConfigMap; every call gets its own uid, as two clusters do.
func cm(ns, name, value string) unstructured.Unstructured {
	uids.Add(1)
	u := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"namespace": ns, "name": name, "uid": fmt.Sprintf("uid-%d", uids.Load())},
		"data":     map[string]any{"k": value},
	}}
	if name == "" {
		delete(u.Object["metadata"].(map[string]any), "name")
	}
	return u
}

var uids atomic.Int64

func states(r setdiff.Report) map[string]string {
	out := make(map[string]string)
	for _, row := range r.Rows {
		out[row.Key] = row.State.String()
	}
	return out
}

func TestCompareMembership(t *testing.T) {
	t.Parallel()
	left := setdiff.Set{Context: "staging", GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{
		cm("shop", "same", "1"), cm("shop", "differs", "1"), cm("shop", "left-only", "1"),
	}}
	right := setdiff.Set{Context: "prod", GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{
		cm("shop", "same", "1"), cm("shop", "differs", "2"), cm("shop", "right-only", "1"),
	}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"same": "equal", "differs": "changed", "left-only": "only left", "right-only": "only right"}
	got := states(r)
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// Changed first, equal last.
	if r.Rows[0].Key != "differs" || r.Rows[len(r.Rows)-1].Key != "same" {
		t.Errorf("order %v", r.Rows)
	}
	if r.Summary() != "1 changed, 1 only left, 1 only right, 0 unavailable, 1 equal" {
		t.Errorf("Summary = %q", r.Summary())
	}
	if r.PairedNamespaces {
		t.Error("same namespace marked as paired")
	}
	// The uid differs on every pair; the config view ignores it, the raw
	// view does not.
	raw, err := setdiff.Compare(left, right, manifestdiff.Options{Mode: manifestdiff.ModeRaw})
	if err != nil {
		t.Fatal(err)
	}
	if states(raw)["same"] != "changed" {
		t.Errorf("raw view: same = %q, want changed by uid", states(raw)["same"])
	}
}

func TestComparePairsNamespaces(t *testing.T) {
	t.Parallel()
	left := setdiff.Set{GVR: cmGVR, Namespace: "staging", Objects: []unstructured.Unstructured{cm("staging", "app", "1")}}
	right := setdiff.Set{GVR: cmGVR, Namespace: "prod", Objects: []unstructured.Unstructured{cm("prod", "app", "1")}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.PairedNamespaces || states(r)["app"] != "equal" {
		t.Fatalf("paired %v states %v, want equal with the namespace left out", r.PairedNamespaces, states(r))
	}
	// The objects themselves keep their namespace.
	if left.Objects[0].GetNamespace() != "staging" || r.Rows[0].Left.Namespace != "staging" || r.Rows[0].Right.Namespace != "prod" {
		t.Error("namespace lost from the input or the sides")
	}
}

func TestCompareAllNamespaces(t *testing.T) {
	t.Parallel()
	left := setdiff.Set{GVR: cmGVR, AllNamespaces: true, Objects: []unstructured.Unstructured{cm("a", "x", "1"), cm("b", "x", "1")}}
	right := setdiff.Set{GVR: cmGVR, AllNamespaces: true, Objects: []unstructured.Unstructured{cm("a", "x", "1")}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := states(r); got["a/x"] != "equal" || got["b/x"] != "only left" || len(got) != 2 {
		t.Errorf("states %v", got)
	}
}

func TestCompareUnavailable(t *testing.T) {
	t.Parallel()
	left := setdiff.Set{GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{
		cm("shop", "twice", "1"), cm("shop", "twice", "2"), cm("shop", "", "1"), cm("shop", "fine", "1"),
	}}
	right := setdiff.Set{GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{cm("shop", "twice", "1"), cm("shop", "fine", "1")}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := states(r)
	// A duplicated key is unavailable on both sides, never "only right".
	if got["twice"] != "unavailable" || got["(no name)"] != "unavailable" || got["fine"] != "equal" || len(got) != 3 {
		t.Errorf("states %v", got)
	}
	if r.Counts[setdiff.Unavailable] != 2 || r.Counts[setdiff.OnlyRight] != 0 {
		t.Errorf("counts %v", r.Counts)
	}
	for _, row := range r.Rows {
		if row.State == setdiff.Unavailable && row.Reason == "" {
			t.Errorf("unavailable row %q without a reason", row.Key)
		}
	}
}

func TestCompareRefusesIncompatibleSets(t *testing.T) {
	t.Parallel()
	v1beta1 := cmGVR
	v1beta1.Version = "v1beta1"
	for _, tc := range []struct {
		name        string
		left, right setdiff.Set
		want        string
	}{
		{name: "versions", left: setdiff.Set{GVR: cmGVR}, right: setdiff.Set{GVR: v1beta1}, want: "different resources or API versions"},
		{name: "scope", left: setdiff.Set{GVR: cmGVR, AllNamespaces: true}, right: setdiff.Set{GVR: cmGVR, Namespace: "a"}, want: "all namespaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := setdiff.Compare(tc.left, tc.right, manifestdiff.Options{})
			if !errors.Is(err, setdiff.ErrIncompatible) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestSetLabel(t *testing.T) {
	t.Parallel()
	deploy := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	for _, tc := range []struct {
		set  setdiff.Set
		want string
	}{
		{setdiff.Set{Context: "prod", GVR: deploy, Namespace: "shop"}, "prod: deployments.apps in shop"},
		{setdiff.Set{Context: "prod", GVR: cmGVR, AllNamespaces: true}, "prod: configmaps in all namespaces"},
		{setdiff.Set{GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}}, "-: nodes in cluster"},
	} {
		if got := tc.set.Label(); got != tc.want {
			t.Errorf("Label = %q, want %q", got, tc.want)
		}
	}
}
