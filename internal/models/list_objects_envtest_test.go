package models

import (
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// ListObjects reads what an object list would show: in a namespace the
// user may read through a namespace cache, and refuses with an error, not
// an empty list, where the user may not read.
func TestListObjectsFollowsListAccess(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	e := newNamespaceUserEnv(t, "list-objects-user", "lo-a")
	createNamespace(t, e.admin, "lo-b")
	createPod(t, e.admin, "lo-a", "web")
	createPod(t, e.admin, "lo-b", "hidden")

	objs, err := ListObjects(t.Context(), e.deps, podGVR, "lo-a")
	if err != nil {
		t.Fatalf("ListObjects in a granted namespace: %v", err)
	}
	if len(objs) != 1 || objs[0].GetName() != "web" {
		t.Fatalf("objects = %d, want the one pod of lo-a", len(objs))
	}
	for _, ns := range []string{"lo-b", AllNamespaces} {
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
