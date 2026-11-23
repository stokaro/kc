package setdiff_test

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/setdiff"
)

// members selects the given objects as a panel shows them.
func members(objs ...unstructured.Unstructured) []setdiff.Member {
	out := make([]setdiff.Member, 0, len(objs))
	for _, o := range objs {
		out = append(out, setdiff.Member{Namespace: o.GetNamespace(), Name: o.GetName(), UID: o.GetUID()})
	}
	return out
}

func tenConfigMaps(ns, value string) []unstructured.Unstructured {
	out := make([]unstructured.Unstructured, 0, 10)
	for i := range 10 {
		out = append(out, cm(ns, fmt.Sprintf("cm-%d", i), value))
	}
	return out
}

func TestCompareVisibleAgainstCounterparts(t *testing.T) {
	t.Parallel()
	lObjs := tenConfigMaps("shop", "1")
	rObjs := tenConfigMaps("shop", "1")
	rObjs[1] = cm("shop", "cm-1", "2")
	visible := setdiff.Selection{Scope: setdiff.Visible, Members: members(lObjs[0], lObjs[1]), Filter: "cm-[01]"}
	left := setdiff.Set{Context: "staging", GVR: cmGVR, Namespace: "shop", Objects: lObjs, Selection: visible}
	right := setdiff.Set{Context: "prod", GVR: cmGVR, Namespace: "shop", Objects: rObjs, Selection: setdiff.Selection{Scope: setdiff.Counterparts}}

	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := states(r); len(got) != 2 || got["cm-0"] != "equal" || got["cm-1"] != "changed" {
		t.Errorf("visible mode states %v, want cm-0 equal and cm-1 changed only", got)
	}
	if got, want := r.Scopes(), "left: visible 2 of 10 (filter: cm-[01]); right: counterparts 2 of 10"; got != want {
		t.Errorf("Scopes = %q, want %q", got, want)
	}

	// The whole-list mode compares all ten.
	left.Selection, right.Selection = setdiff.Selection{}, setdiff.Selection{}
	whole, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Rows) != 10 || whole.Counts[setdiff.Changed] != 1 || whole.Counts[setdiff.Equal] != 9 {
		t.Errorf("whole mode counts %v over %d rows", whole.Counts, len(whole.Rows))
	}
	if got := whole.Scopes(); got != "left: whole list 10; right: whole list 10" {
		t.Errorf("Scopes = %q", got)
	}
}

// A counterpart missing on the other side is genuinely only on the
// selecting side.
func TestCompareMissingCounterpartIsOnlyLeft(t *testing.T) {
	t.Parallel()
	a, b := cm("shop", "a", "1"), cm("shop", "b", "1")
	left := setdiff.Set{GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{a, b}, Selection: setdiff.Selection{Scope: setdiff.Marked, Members: members(a, b)}}
	right := setdiff.Set{GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{cm("shop", "a", "1"), cm("shop", "c", "1")}, Selection: setdiff.Selection{Scope: setdiff.Counterparts}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := states(r); len(got) != 2 || got["a"] != "equal" || got["b"] != "only left" {
		t.Errorf("states %v", got)
	}
}

// A marked row whose object was deleted or replaced before the read is
// unavailable with the reason, never "only right".
func TestCompareMarkedChangedDuringRead(t *testing.T) {
	t.Parallel()
	kept, deleted, replaced := cm("shop", "kept", "1"), cm("shop", "deleted", "1"), cm("shop", "replaced", "1")
	sel := setdiff.Selection{Scope: setdiff.Marked, Members: members(kept, deleted, replaced)}
	// The read sees "replaced" as a new instance and "deleted" is gone.
	left := setdiff.Set{Context: "a", GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{kept, cm("shop", "replaced", "1"), cm("shop", "other", "1")}, Selection: sel}
	right := setdiff.Set{Context: "b", GVR: cmGVR, Namespace: "shop", Objects: []unstructured.Unstructured{cm("shop", "kept", "1"), cm("shop", "deleted", "1"), cm("shop", "replaced", "1")}, Selection: setdiff.Selection{Scope: setdiff.Counterparts}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := states(r)
	if len(got) != 3 || got["kept"] != "equal" || got["deleted"] != "unavailable" || got["replaced"] != "unavailable" {
		t.Fatalf("states %v", got)
	}
	for _, row := range r.Rows {
		switch row.Key {
		case "deleted":
			if !strings.Contains(row.Reason, "deleted after it was selected") {
				t.Errorf("deleted reason %q", row.Reason)
			}
		case "replaced":
			if !strings.Contains(row.Reason, "replaced after it was selected") || !strings.Contains(row.Reason, string(replaced.GetUID())) {
				t.Errorf("replaced reason %q", row.Reason)
			}
		}
	}
	if r.LeftCount.Lost != 2 || r.LeftCount.Compared != 1 || !strings.Contains(r.Scopes(), "2 gone since selected") {
		t.Errorf("left count %+v, scopes %q", r.LeftCount, r.Scopes())
	}
}

// Both panels narrowed: each keeps its own selection, so an object one side
// did not select is only on the other side.
func TestCompareTwoSelections(t *testing.T) {
	t.Parallel()
	a1, b1 := cm("x", "a", "1"), cm("x", "b", "1")
	a2, c2 := cm("y", "a", "1"), cm("y", "c", "1")
	left := setdiff.Set{GVR: cmGVR, Namespace: "x", Objects: []unstructured.Unstructured{a1, b1}, Selection: setdiff.Selection{Scope: setdiff.Marked, Members: members(a1)}}
	right := setdiff.Set{GVR: cmGVR, Namespace: "y", Objects: []unstructured.Unstructured{a2, c2}, Selection: setdiff.Selection{Scope: setdiff.Visible, Members: members(a2, c2)}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := states(r); len(got) != 2 || got["a"] != "equal" || got["c"] != "only right" {
		t.Errorf("states %v", got)
	}
}

// Across all namespaces the same name in two namespaces stays two
// members; a selection by name alone would merge them.
func TestCompareMarkedDuplicateNamesAcrossNamespaces(t *testing.T) {
	t.Parallel()
	a, b := cm("a", "web", "1"), cm("b", "web", "1")
	left := setdiff.Set{GVR: cmGVR, AllNamespaces: true, Objects: []unstructured.Unstructured{a, b}, Selection: setdiff.Selection{Scope: setdiff.Marked, Members: members(b)}}
	right := setdiff.Set{GVR: cmGVR, AllNamespaces: true, Objects: []unstructured.Unstructured{cm("a", "web", "2"), cm("b", "web", "1")}, Selection: setdiff.Selection{Scope: setdiff.Counterparts}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := states(r); len(got) != 1 || got["b/web"] != "equal" {
		t.Errorf("states %v, want only b/web", got)
	}
}

// A counterparts side facing a side without a selection compares its whole
// list and says so.
func TestCompareCounterpartsWithoutSelection(t *testing.T) {
	t.Parallel()
	left := setdiff.Set{GVR: cmGVR, Namespace: "x", Objects: []unstructured.Unstructured{cm("x", "a", "1")}}
	right := setdiff.Set{GVR: cmGVR, Namespace: "x", Objects: []unstructured.Unstructured{cm("x", "a", "1"), cm("x", "b", "1")}, Selection: setdiff.Selection{Scope: setdiff.Counterparts}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if states(r)["b"] != "only right" || r.RightCount.Scope != setdiff.Whole {
		t.Errorf("states %v, right %+v", states(r), r.RightCount)
	}
}
