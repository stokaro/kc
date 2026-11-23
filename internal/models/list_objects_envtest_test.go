package models

import (
	"strings"
	"testing"

	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// ListObjects reads what an object list would show: in a namespace the
// user may read through a namespace cache, and refuses with an error, not
// an empty list, where the user may not read.
func TestListObjectsFollowsListAccess(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	loA, loB := kctesting.UniqueName("lo-a"), kctesting.UniqueName("lo-b")
	e := newNamespaceUserEnv(t, kctesting.UniqueName("list-objects-user"), loA)
	createNamespace(t, e.admin, loB)
	createPod(t, e.admin, loA, "web")
	createPod(t, e.admin, loB, "hidden")

	objs, err := ListObjects(t.Context(), e.deps, podGVR, loA)
	if err != nil {
		t.Fatalf("ListObjects in a granted namespace: %v", err)
	}
	if len(objs) != 1 || objs[0].GetName() != "web" {
		t.Fatalf("objects = %d, want the one pod of lo-a", len(objs))
	}
	for _, ns := range []string{loB, AllNamespaces} {
		objs, err := ListObjects(t.Context(), e.deps, podGVR, ns)
		if err == nil || !strings.Contains(err.Error(), "not permitted") {
			t.Errorf("ListObjects(%q) = %d objects, %v; want a refusal", ns, len(objs), err)
		}
	}
	// The namespace cache acquired for lo-a is released on return.
	if holds := e.openHolds(); len(holds) != 0 {
		t.Errorf("open holds after ListObjects: %v", holds)
	}
}
