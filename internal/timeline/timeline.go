// Package timeline records the revisions of one Kubernetes object that kc
// observes while a recording runs, and renders the change between two of
// them with the manifest diff.
//
// A timeline holds only what kc saw: notifications from the shared cache
// after the recording started. It is bounded by a number of entries and a
// number of bytes; the oldest entries are evicted first. resourceVersion is
// an opaque token: it is compared for equality only, never parsed, sorted or
// compared across objects. Entries are ordered by when kc observed them.
package timeline

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/manifestdiff"
)

// Kind says what an entry records.
type Kind int

const (
	// KindInitial is the object as the recording first saw it.
	KindInitial Kind = iota
	// KindChange is a later revision of the same object (same UID).
	KindChange
	// KindRecreated is the first revision of an object with the same name
	// and a new UID: a new lifetime.
	KindRecreated
	// KindDeleted is the deletion of the object.
	KindDeleted
	// KindGap marks a stretch kc may not have observed.
	KindGap
)

func (k Kind) String() string {
	switch k {
	case KindInitial:
		return "initial"
	case KindChange:
		return "change"
	case KindRecreated:
		return "recreated"
	case KindDeleted:
		return "deleted"
	}
	return "gap"
}

// Identity is the object a timeline follows. The UID of each lifetime is on
// its entries.
type Identity struct {
	Context   string
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
}

// Limits bound a timeline. A value of zero or less means no limit of that
// kind; New keeps at least one entry.
type Limits struct {
	MaxEntries int
	MaxBytes   int
}

// Entry is one recorded event.
type Entry struct {
	Seq             int // observation order within the timeline, from 1
	Kind            Kind
	UID             types.UID
	ResourceVersion string // opaque
	Observed        time.Time
	// Object is the normalized object (managedFields removed); nil for a
	// gap and for a deletion whose final state is unknown.
	Object map[string]any
	// Summary lists the notable changes against the previous revision of
	// the same lifetime.
	Summary []string
	// SpecChanged, StatusChanged and MetaChanged say which parts changed:
	// everything outside metadata and status, status, and the labels,
	// annotations, owners, finalizers or deletion of metadata.
	SpecChanged, StatusChanged, MetaChanged bool
	// Note explains a gap or an unknown final state.
	Note string
	size int
}

// Snapshot is a copy of a timeline's state.
type Snapshot struct {
	Identity Identity
	Started  time.Time
	Stopped  time.Time // zero while recording
	Limits   Limits
	Entries  []Entry // oldest first
	Bytes    int
	// Evicted counts entries dropped by the limits; EvictedUntil is the
	// observation time of the newest of them.
	Evicted      int
	EvictedUntil time.Time
	// Duplicates counts notifications with a resourceVersion already
	// recorded; Churn counts revisions that changed only managedFields or
	// the resourceVersion.
	Duplicates, Churn int
}

// Timeline is the recording of one object. It is safe for concurrent use.
type Timeline struct {
	mu       sync.Mutex
	id       Identity
	limits   Limits
	started  time.Time
	stopped  time.Time
	entries  []Entry
	bytes    int
	nextSeq  int
	evicted  int
	evUntil  time.Time
	dups     int
	churn    int
	lastUID  types.UID
	lastRV   string
	lastObj  map[string]any // newest revision, kept even when evicted
	lastGone bool           // the newest event is a deletion
}

// New starts a timeline at now.
func New(id Identity, limits Limits, now time.Time) *Timeline {
	return &Timeline{id: id, limits: limits, started: now, nextSeq: 1}
}

// Observe records obj, the object as a notification delivered it. It
// returns false when the notification adds nothing: a resourceVersion the
// timeline already has for this UID, or a change of managedFields only.
func (t *Timeline) Observe(obj map[string]any, now time.Time) bool {
	u := &unstructured.Unstructured{Object: obj}
	uid, rv := u.GetUID(), u.GetResourceVersion()
	norm := Normalize(obj)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped.IsZero() {
		return false
	}
	sameLife := t.lastObj != nil && uid == t.lastUID && !t.lastGone
	if sameLife && rv == t.lastRV {
		t.dups++
		return false
	}
	if sameLife && reflect.DeepEqual(withoutVersion(norm), withoutVersion(t.lastObj)) {
		t.churn++
		t.lastRV = rv
		return false
	}
	e := Entry{Kind: KindChange, UID: uid, ResourceVersion: rv, Observed: now, Object: norm}
	switch {
	case t.lastObj == nil:
		e.Kind = KindInitial
		e.Summary = []string{"first observed revision"}
	case uid != t.lastUID:
		e.Kind = KindRecreated
		e.Summary = []string{"new object with this name, uid " + string(uid)}
	default:
		// The same UID after a deletion is the same lifetime seen again
		// (a deletion kc only presumed); compare with the last revision.
		e.SpecChanged, e.StatusChanged, e.MetaChanged = changedParts(t.lastObj, norm)
		e.Summary = Summarize(t.lastObj, norm)
	}
	t.lastUID, t.lastRV, t.lastObj, t.lastGone = uid, rv, norm, false
	t.appendLocked(e)
	return true
}

// Deleted records the deletion of the object. obj is its last state, or nil
// when the cache only knows that it is gone (a deletion seen after a gap).
func (t *Timeline) Deleted(obj map[string]any, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped.IsZero() || t.lastGone {
		return
	}
	e := Entry{Kind: KindDeleted, Observed: now, UID: t.lastUID, Summary: []string{"deleted"}}
	if obj != nil {
		u := &unstructured.Unstructured{Object: obj}
		e.UID, e.ResourceVersion = u.GetUID(), u.GetResourceVersion()
		e.Object = Normalize(obj)
	} else {
		e.Note = "the cache reported the object gone without its final state; changes before the deletion may be missing"
	}
	t.lastGone = true
	t.appendLocked(e)
}

// Gap records that kc may have missed revisions, with the reason.
func (t *Timeline) Gap(reason string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped.IsZero() {
		return
	}
	t.appendLocked(Entry{Kind: KindGap, Observed: now, Note: reason, Summary: []string{reason}})
}

// Stop ends the recording; later notifications are ignored.
func (t *Timeline) Stop(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped.IsZero() {
		t.stopped = now
	}
}

// Snapshot returns a copy of the timeline. Entry objects are shared and must
// not be modified.
func (t *Timeline) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Snapshot{
		Identity: t.id, Started: t.started, Stopped: t.stopped, Limits: t.limits,
		Entries: slices.Clone(t.entries), Bytes: t.bytes,
		Evicted: t.evicted, EvictedUntil: t.evUntil, Duplicates: t.dups, Churn: t.churn,
	}
}

func (t *Timeline) appendLocked(e Entry) {
	e.Seq = t.nextSeq
	t.nextSeq++
	if e.Object != nil {
		if b, err := json.Marshal(e.Object); err == nil {
			e.size = len(b)
		}
	}
	t.entries = append(t.entries, e)
	t.bytes += e.size
	for len(t.entries) > 1 && t.overLimitLocked() {
		old := t.entries[0]
		t.entries = t.entries[1:]
		t.bytes -= old.size
		t.evicted++
		t.evUntil = old.Observed
	}
}

func (t *Timeline) overLimitLocked() bool {
	return (t.limits.MaxEntries > 0 && len(t.entries) > t.limits.MaxEntries) ||
		(t.limits.MaxBytes > 0 && t.bytes > t.limits.MaxBytes)
}

// Normalize returns a copy of obj without metadata.managedFields, the
// server's record of field owners, which changes on every apply and says
// nothing about the object.
func Normalize(obj map[string]any) map[string]any {
	out := maps.Clone(obj)
	if meta, ok := out["metadata"].(map[string]any); ok {
		m := maps.Clone(meta)
		delete(m, "managedFields")
		out["metadata"] = m
	}
	return out
}

func withoutVersion(obj map[string]any) map[string]any {
	out := maps.Clone(obj)
	if meta, ok := out["metadata"].(map[string]any); ok {
		m := maps.Clone(meta)
		delete(m, "resourceVersion")
		out["metadata"] = m
	}
	return out
}

// metaFields are the metadata fields whose change is a change of the object.
var metaFields = []string{"labels", "annotations", "ownerReferences", "finalizers", "deletionTimestamp"}

func changedParts(prev, cur map[string]any) (spec, status, meta bool) {
	keys := make(map[string]bool)
	for k := range prev {
		keys[k] = true
	}
	for k := range cur {
		keys[k] = true
	}
	for k := range keys {
		switch k {
		case "metadata", "status", "apiVersion", "kind":
		default:
			if !reflect.DeepEqual(prev[k], cur[k]) {
				spec = true
			}
		}
	}
	status = !reflect.DeepEqual(prev["status"], cur["status"])
	pm, _ := prev["metadata"].(map[string]any)
	cm, _ := cur["metadata"].(map[string]any)
	for _, f := range metaFields {
		if !reflect.DeepEqual(pm[f], cm[f]) {
			meta = true
		}
	}
	return spec, status, meta
}

// Summarize lists the notable differences from prev to cur: generation,
// images, replica counts, phase, conditions, restart counts and deletion.
// It falls back to naming the changed parts.
func Summarize(prev, cur map[string]any) []string {
	var out []string
	add := func(what string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			out = append(out, fmt.Sprintf("%s %s -> %s", what, show(a), show(b)))
		}
	}
	add("generation", nested(prev, "metadata", "generation"), nested(cur, "metadata", "generation"))
	out = append(out, imageChanges(prev, cur)...)
	add("spec.replicas", nested(prev, "spec", "replicas"), nested(cur, "spec", "replicas"))
	for _, f := range []string{"readyReplicas", "availableReplicas", "updatedReplicas", "numberUnavailable"} {
		add("status."+f, nested(prev, "status", f), nested(cur, "status", f))
	}
	add("status.phase", nested(prev, "status", "phase"), nested(cur, "status", "phase"))
	out = append(out, conditionChanges(prev, cur)...)
	out = append(out, restartChanges(prev, cur)...)
	if nested(prev, "metadata", "deletionTimestamp") == nil && nested(cur, "metadata", "deletionTimestamp") != nil {
		out = append(out, "deletion requested")
	}
	if len(out) > 0 {
		return out
	}
	spec, status, meta := changedParts(prev, cur)
	if spec {
		out = append(out, "spec changed")
	}
	if meta {
		out = append(out, "labels, annotations, owners or finalizers changed")
	}
	if status {
		out = append(out, "status changed")
	}
	if len(out) == 0 {
		out = append(out, "metadata changed")
	}
	return out
}

func nested(obj map[string]any, path ...string) any {
	v, ok, err := unstructured.NestedFieldNoCopy(obj, path...)
	if !ok || err != nil {
		return nil
	}
	return v
}

func show(v any) string {
	if v == nil {
		return "(none)"
	}
	return fmt.Sprint(v)
}

// imageChanges compares the images of the pod spec (of a pod) or the pod
// template (of a workload), per container name.
func imageChanges(prev, cur map[string]any) []string {
	p, c := images(prev), images(cur)
	names := make([]string, 0, len(p)+len(c))
	for n := range p {
		names = append(names, n)
	}
	for n := range c {
		if _, ok := p[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if p[n] != c[n] {
			out = append(out, fmt.Sprintf("image %s %s -> %s", n, orNone(p[n]), orNone(c[n])))
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func images(obj map[string]any) map[string]string {
	out := make(map[string]string)
	for _, base := range [][]string{{"spec"}, {"spec", "template", "spec"}, {"spec", "jobTemplate", "spec", "template", "spec"}} {
		for _, field := range []string{"initContainers", "containers"} {
			list, _ := nested(obj, append(slices.Clone(base), field)...).([]any)
			for _, item := range list {
				m, _ := item.(map[string]any)
				name, _ := m["name"].(string)
				image, _ := m["image"].(string)
				if name != "" {
					out[name] = image
				}
			}
		}
	}
	return out
}

func conditionChanges(prev, cur map[string]any) []string {
	p, c := conditions(prev), conditions(cur)
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	for k := range p {
		if _, ok := c[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if p[k] != c[k] {
			out = append(out, fmt.Sprintf("condition %s %s -> %s", k, orNone(p[k]), orNone(c[k])))
		}
	}
	return out
}

func conditions(obj map[string]any) map[string]string {
	out := make(map[string]string)
	list, _ := nested(obj, "status", "conditions").([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		t, _ := m["type"].(string)
		if t == "" {
			continue
		}
		s, _ := m["status"].(string)
		if r, _ := m["reason"].(string); r != "" {
			s += " " + r
		}
		out[t] = s
	}
	return out
}

func restartChanges(prev, cur map[string]any) []string {
	p, c := restarts(prev), restarts(cur)
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if before, ok := p[n]; ok && before != c[n] {
			out = append(out, fmt.Sprintf("restarts %s %d -> %d", n, before, c[n]))
		}
	}
	return out
}

func restarts(obj map[string]any) map[string]int64 {
	out := make(map[string]int64)
	for _, field := range []string{"initContainerStatuses", "containerStatuses"} {
		list, _ := nested(obj, "status", field).([]any)
		for _, item := range list {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			if name == "" {
				continue
			}
			switch n := m["restartCount"].(type) {
			case int64:
				out[name] = n
			case float64:
				out[name] = int64(n)
			}
		}
	}
	return out
}

// Part selects what Diff compares.
type Part int

const (
	// PartConfig compares everything but status and the server fields.
	PartConfig Part = iota
	// PartStatus compares status only.
	PartStatus
)

// Previous returns the retained revision that entry i of s follows: the
// nearest earlier entry with an object of the same UID. ok is false when the
// entry starts a lifetime or its predecessor was evicted.
func (s Snapshot) Previous(i int) (Entry, bool) {
	if i <= 0 || i >= len(s.Entries) {
		return Entry{}, false
	}
	e := s.Entries[i]
	if e.Kind == KindInitial || e.Kind == KindRecreated || e.Kind == KindGap {
		return Entry{}, false
	}
	for j := i - 1; j >= 0; j-- {
		p := s.Entries[j]
		if p.Object != nil && p.UID == e.UID && p.Kind != KindDeleted {
			return p, true
		}
	}
	return Entry{}, false
}

// Diff renders entry i of s against its previous revision with the manifest
// diff. Without a retained previous revision it shows the entry against an
// empty object and says why.
func (s Snapshot) Diff(i int, part Part) (string, error) {
	if i < 0 || i >= len(s.Entries) {
		return "", fmt.Errorf("no entry %d", i)
	}
	e := s.Entries[i]
	if e.Object == nil {
		return "# " + e.Kind.String() + ": " + e.Note, nil
	}
	prev, ok := s.Previous(i)
	note := ""
	left := s.side(Entry{}, "(none)")
	if ok {
		left = s.side(prev, "")
	} else {
		switch e.Kind {
		case KindInitial, KindRecreated:
			note = "# The first revision of this lifetime kc observed; there is nothing earlier to compare."
		default:
			note = "# The previous revision was evicted by the timeline limits."
		}
	}
	right := s.side(e, "")
	opts := manifestdiff.Options{Mode: manifestdiff.ModeConfig}
	if part == PartStatus {
		opts.IncludeStatus = true
		left.Object = statusOnly(left.Object)
		right.Object = statusOnly(right.Object)
	}
	res, err := manifestdiff.Compare(left, right, opts)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("# revision %d (%s), observed %s, uid %s, resourceVersion %s",
		e.Seq, e.Kind, e.Observed.UTC().Format(time.RFC3339), e.UID, e.ResourceVersion)
	parts := []string{header}
	if ok {
		parts = append(parts, fmt.Sprintf("# against revision %d, observed %s, resourceVersion %s", prev.Seq, prev.Observed.UTC().Format(time.RFC3339), prev.ResourceVersion))
	}
	if note != "" {
		parts = append(parts, note)
	}
	return strings.Join(append(parts, res.Text), "\n"), nil
}

func (s Snapshot) side(e Entry, suffix string) manifestdiff.Side {
	name := s.Identity.Name
	if suffix != "" {
		name += " " + suffix
	} else if e.ResourceVersion != "" {
		name += "@" + e.ResourceVersion
	}
	return manifestdiff.Side{Context: s.Identity.Context, GVR: s.Identity.GVR, Namespace: s.Identity.Namespace, Name: name, Object: e.Object}
}

func statusOnly(obj map[string]any) map[string]any {
	if obj == nil {
		return nil
	}
	if st, ok := obj["status"]; ok {
		return map[string]any{"status": st}
	}
	return make(map[string]any)
}
