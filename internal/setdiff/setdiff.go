// Package setdiff compares two sets of objects of one resource type, such
// as the lists of two panels or one list in two contexts. Objects are
// matched by name, or by namespace and name when both sets span all
// namespaces. Each match is compared with the normalized manifest diff, so
// "changed" means what the compare viewer would show.
package setdiff

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/manifestdiff"
)

// Set is one side of a comparison.
type Set struct {
	// Context is the kubeconfig context the objects were read from.
	Context string
	GVR     schema.GroupVersionResource
	// Namespace is the namespace of a namespaced list; empty for a list of
	// all namespaces or of a cluster-scoped type (see AllNamespaces).
	Namespace string
	// AllNamespaces is true for a namespaced type listed across all
	// namespaces. Objects are then matched by namespace and name.
	AllNamespaces bool
	Objects       []unstructured.Unstructured
	// Selection narrows Objects to what the panel showed or marked; the
	// zero value compares the whole list.
	Selection Selection
}

// Label is "ctx: deployments.apps in shop", for the heading.
func (s Set) Label() string {
	where := "cluster"
	switch {
	case s.AllNamespaces:
		where = "all namespaces"
	case s.Namespace != "":
		where = s.Namespace
	}
	ctx := s.Context
	if ctx == "" {
		ctx = "-"
	}
	return ctx + ": " + resourceName(s.GVR) + " in " + where
}

func resourceName(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Group
}

// State is the outcome for one key.
type State int

// The states, in the order the report lists them.
const (
	Changed State = iota
	OnlyLeft
	OnlyRight
	Unavailable
	Equal
)

func (s State) String() string {
	switch s {
	case Changed:
		return "changed"
	case OnlyLeft:
		return "only left"
	case OnlyRight:
		return "only right"
	case Unavailable:
		return "unavailable"
	default:
		return "equal"
	}
}

// Row is the outcome for one object key.
type Row struct {
	// Key is the name, or namespace/name when the sets span all namespaces.
	Key   string
	State State
	// Left and Right are the matched objects; nil on the side that lacks
	// the key.
	Left, Right *manifestdiff.Side
	// Reason says why a row is unavailable.
	Reason string
}

// Report is the result of Compare.
type Report struct {
	Left, Right Set
	Rows        []Row
	// Counts holds the rows per state.
	Counts map[State]int
	// LeftCount and RightCount say what each side evaluated.
	LeftCount, RightCount SideCount
	// Options are the manifest view the pairs were compared in.
	Options manifestdiff.Options
	// PairedNamespaces is true when two different namespaces were paired:
	// metadata.namespace is then left out of every pair, since it differs
	// by intent.
	PairedNamespaces bool
}

// Summary is "2 changed, 1 only left, 0 only right, 0 unavailable, 9 equal".
func (r Report) Summary() string {
	parts := make([]string, 0, 5)
	for _, s := range []State{Changed, OnlyLeft, OnlyRight, Unavailable, Equal} {
		parts = append(parts, fmt.Sprintf("%d %s", r.Counts[s], s))
	}
	return strings.Join(parts, ", ")
}

// Scopes is "left: marked 2 of 10; right: counterparts 2 of 12".
func (r Report) Scopes() string {
	return "left: " + r.LeftCount.String() + "; right: " + r.RightCount.String()
}

// ErrIncompatible is returned when two sets cannot be matched.
var ErrIncompatible = errors.New("sets cannot be compared")

// Compare matches left and right and compares each pair in opts. The sets
// must list the same group, version and resource: kc does not convert
// between API versions. A list of all namespaces only pairs with another
// list of all namespaces; two namespaces pair with each other by name.
func Compare(left, right Set, opts manifestdiff.Options) (Report, error) {
	if left.GVR != right.GVR {
		return Report{}, fmt.Errorf("%w: %s and %s are different resources or API versions", ErrIncompatible, gvrString(left.GVR), gvrString(right.GVR))
	}
	if left.AllNamespaces != right.AllNamespaces {
		return Report{}, fmt.Errorf("%w: one side lists all namespaces, the other one namespace; open the same scope on both sides", ErrIncompatible)
	}
	lost, lc, rc := applyScopes(&left, &right)
	r := Report{Left: left, Right: right, Counts: make(map[State]int), Options: opts, LeftCount: lc, RightCount: rc}
	r.PairedNamespaces = !left.AllNamespaces && left.Namespace != right.Namespace
	ls, lBad, lDup := index(left, r.PairedNamespaces)
	rs, rBad, rDup := index(right, r.PairedNamespaces)
	// A selected row whose object went away during the read is not
	// missing on its side: it cannot be compared on either.
	for _, key := range slices.Sorted(maps.Keys(lost)) {
		delete(ls, key)
		delete(rs, key)
		delete(lDup, key)
		delete(rDup, key)
		lBad = append(lBad, Row{Key: key, State: Unavailable, Reason: lost[key]})
	}
	// A key that occurs twice on one side cannot be matched on either.
	for key, where := range mergeDups(lDup, rDup) {
		delete(ls, key)
		delete(rs, key)
		lBad = append(lBad, Row{Key: key, State: Unavailable, Reason: key + " occurs more than once in " + where})
	}
	keys := make([]string, 0, len(ls)+len(rs))
	for k := range ls {
		keys = append(keys, k)
	}
	for k := range rs {
		if _, ok := ls[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		l, inL := ls[k]
		rt, inR := rs[k]
		row := Row{Key: k}
		switch {
		case !inR:
			row.State, row.Left = OnlyLeft, &l
		case !inL:
			row.State, row.Right = OnlyRight, &rt
		default:
			row.Left, row.Right = &l, &rt
			res, err := manifestdiff.Compare(l, rt, opts)
			switch {
			case err != nil:
				row.State, row.Reason = Unavailable, err.Error()
			case res.Equal:
				row.State = Equal
			default:
				row.State = Changed
			}
		}
		r.Rows = append(r.Rows, row)
	}
	r.Rows = append(r.Rows, lBad...)
	r.Rows = append(r.Rows, rBad...)
	slices.SortStableFunc(r.Rows, func(a, b Row) int {
		if a.State != b.State {
			return int(a.State) - int(b.State)
		}
		return strings.Compare(a.Key, b.Key)
	})
	for _, row := range r.Rows {
		r.Counts[row.State]++
	}
	return r, nil
}

// mergeDups returns the duplicated keys of both sides with the label of the
// side they repeat in.
func mergeDups(sides ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range sides {
		for k, v := range m {
			if prev, ok := out[k]; ok {
				v = prev + " and " + v
			}
			out[k] = v
		}
	}
	return out
}

// index keys the objects of s. Objects without a name, and keys that occur
// twice, cannot be matched: they become unavailable rows (dups maps each
// repeated key to the set label) instead of being counted as missing or
// equal.
func index(s Set, dropNamespace bool) (out map[string]manifestdiff.Side, bad []Row, dups map[string]string) {
	out = make(map[string]manifestdiff.Side, len(s.Objects))
	dups = make(map[string]string)
	for _, o := range s.Objects {
		name := o.GetName()
		if name == "" {
			bad = append(bad, Row{Key: "(no name)", State: Unavailable, Reason: "an object of " + s.Label() + " has no name"})
			continue
		}
		key := name
		if s.AllNamespaces {
			key = o.GetNamespace() + "/" + name
		}
		if _, ok := out[key]; ok {
			dups[key] = s.Label()
			continue
		}
		obj := o.Object
		if dropNamespace {
			obj = withoutNamespace(obj)
		}
		out[key] = manifestdiff.Side{Context: s.Context, GVR: s.GVR, Namespace: o.GetNamespace(), Name: name, Object: obj}
	}
	return out, bad, dups
}

func gvrString(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Version + "/" + gvr.Resource
	}
	return gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
}

// withoutNamespace returns obj without metadata.namespace. obj is not
// modified; nested values are shared.
func withoutNamespace(obj map[string]any) map[string]any {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return obj
	}
	out := maps.Clone(obj)
	m := maps.Clone(meta)
	delete(m, "namespace")
	out["metadata"] = m
	return out
}
