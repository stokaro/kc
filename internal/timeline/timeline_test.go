package timeline_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/timeline"
)

var (
	t0 = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	id = timeline.Identity{
		Context: "kind-dev", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Namespace: "shop", Name: "web",
	}
)

// deployment is a Deployment manifest as the cache delivers it.
func deployment(uid, rv, image string, generation int64, mut func(map[string]any)) map[string]any {
	obj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name": "web", "namespace": "shop", "uid": uid, "resourceVersion": rv, "generation": generation,
			"managedFields": []any{map[string]any{"manager": "kubectl", "time": "2026-11-23T11:00:00Z"}},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"name": "app", "image": image},
			}}},
		},
		"status": map[string]any{
			"readyReplicas": int64(2),
			"conditions":    []any{map[string]any{"type": "Available", "status": "True", "reason": "MinimumReplicasAvailable"}},
		},
	}
	if mut != nil {
		mut(obj)
	}
	return obj
}

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

func kinds(s timeline.Snapshot) []string {
	out := make([]string, 0, len(s.Entries))
	for _, e := range s.Entries {
		out = append(out, e.Kind.String())
	}
	return out
}

func unavailable(obj map[string]any) {
	st := obj["status"].(map[string]any)
	st["readyReplicas"] = int64(1)
	st["conditions"] = []any{map[string]any{"type": "Available", "status": "False", "reason": "MinimumReplicasUnavailable"}}
}

func TestImageChangeHasIdentityAndDiff(t *testing.T) {
	t.Parallel()
	tl := timeline.New(id, timeline.Limits{}, t0)
	tl.Observe(deployment("u1", "a1", "app:1", 1, nil), at(1))
	if !tl.Observe(deployment("u1", "b2", "app:2", 2, nil), at(2)) {
		t.Fatalf("an image change was not recorded")
	}
	s := tl.Snapshot()
	if got := kinds(s); !slices.Equal(got, []string{"initial", "change"}) {
		t.Fatalf("kinds = %v", got)
	}
	e := s.Entries[1]
	if !slices.Equal(e.Summary, []string{"generation 1 -> 2", "image app app:1 -> app:2"}) {
		t.Fatalf("summary = %q", e.Summary)
	}
	if !e.SpecChanged || e.StatusChanged || e.MetaChanged || e.UID != "u1" || e.ResourceVersion != "b2" || !e.Observed.Equal(at(2)) {
		t.Fatalf("entry = %+v", e)
	}
	diff, err := s.Diff(1, timeline.PartConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# revision 2 (change), observed 2026-11-23T12:00:02Z, uid u1, resourceVersion b2",
		"# against revision 1, observed 2026-11-23T12:00:01Z, resourceVersion a1",
		"context kind-dev, gvr apps/v1/deployments, namespace shop, name web@a1",
		"-      - image: app:1",
		"+      - image: app:2",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff lacks %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "managedFields") || strings.Contains(diff, "readyReplicas") {
		t.Fatalf("the configuration diff shows managedFields or status:\n%s", diff)
	}
}

func TestStatusOnlyTransition(t *testing.T) {
	t.Parallel()
	tl := timeline.New(id, timeline.Limits{}, t0)
	tl.Observe(deployment("u1", "1", "app:1", 1, nil), at(1))
	tl.Observe(deployment("u1", "2", "app:1", 1, unavailable), at(2))
	s := tl.Snapshot()
	e := s.Entries[1]
	if e.SpecChanged || e.MetaChanged || !e.StatusChanged {
		t.Fatalf("parts = spec %v meta %v status %v", e.SpecChanged, e.MetaChanged, e.StatusChanged)
	}
	if want := []string{"status.readyReplicas 2 -> 1", "condition Available True MinimumReplicasAvailable -> False MinimumReplicasUnavailable"}; !slices.Equal(e.Summary, want) {
		t.Fatalf("summary = %q", e.Summary)
	}
	config, _ := s.Diff(1, timeline.PartConfig)
	if !strings.Contains(config, "No differences in the configuration view.") {
		t.Fatalf("a status change shows in the configuration diff:\n%s", config)
	}
	status, _ := s.Diff(1, timeline.PartStatus)
	if !strings.Contains(status, "-  readyReplicas: 2") || !strings.Contains(status, "+  readyReplicas: 1") || strings.Contains(status, "image:") {
		t.Fatalf("status diff:\n%s", status)
	}
}

func TestDuplicatesChurnAndOpaqueVersions(t *testing.T) {
	t.Parallel()
	tl := timeline.New(id, timeline.Limits{}, t0)
	// resourceVersion is not a number here, and its order means nothing.
	tl.Observe(deployment("u1", "zz-9", "app:1", 1, nil), at(1))
	if tl.Observe(deployment("u1", "zz-9", "app:1", 1, nil), at(2)) {
		t.Fatalf("a duplicate notification was recorded")
	}
	churn := deployment("u1", "aa-1", "app:1", 1, func(o map[string]any) {
		o["metadata"].(map[string]any)["managedFields"] = []any{map[string]any{"manager": "other"}}
	})
	if tl.Observe(churn, at(3)) {
		t.Fatalf("a managedFields-only revision was recorded")
	}
	// The churn revision's version is now the known one.
	if tl.Observe(deployment("u1", "aa-1", "app:1", 1, nil), at(4)) {
		t.Fatalf("a duplicate of the churn revision was recorded")
	}
	if !tl.Observe(deployment("u1", "mm-5", "app:2", 2, nil), at(5)) {
		t.Fatalf("a real change after churn was not recorded")
	}
	s := tl.Snapshot()
	if len(s.Entries) != 2 || s.Duplicates != 2 || s.Churn != 1 {
		t.Fatalf("entries %d, duplicates %d, churn %d", len(s.Entries), s.Duplicates, s.Churn)
	}
	if got := []string{s.Entries[0].ResourceVersion, s.Entries[1].ResourceVersion}; !slices.Equal(got, []string{"zz-9", "mm-5"}) {
		t.Fatalf("order = %v, want observation order", got)
	}
}

func TestReplacementStartsANewLifetime(t *testing.T) {
	t.Parallel()
	tl := timeline.New(id, timeline.Limits{}, t0)
	tl.Observe(deployment("u1", "1", "app:1", 1, nil), at(1))
	tl.Deleted(deployment("u1", "2", "app:1", 1, nil), at(2))
	// The new object has the same name and resourceVersion text, and a
	// new UID: not a duplicate, not a change of the old one.
	if !tl.Observe(deployment("u2", "2", "app:9", 1, nil), at(3)) {
		t.Fatalf("the recreated object was not recorded")
	}
	s := tl.Snapshot()
	if got := kinds(s); !slices.Equal(got, []string{"initial", "deleted", "recreated"}) {
		t.Fatalf("kinds = %v", got)
	}
	if _, ok := s.Previous(2); ok {
		t.Fatalf("the new lifetime has a previous revision of the old UID")
	}
	diff, _ := s.Diff(2, timeline.PartConfig)
	if !strings.Contains(diff, "nothing earlier to compare") || !strings.Contains(diff, "uid u2") {
		t.Fatalf("diff:\n%s", diff)
	}
}

func TestGapsAndUnknownDeletion(t *testing.T) {
	t.Parallel()
	tl := timeline.New(id, timeline.Limits{}, t0)
	tl.Observe(deployment("u1", "1", "app:1", 1, nil), at(1))
	tl.Gap("the watch stopped; changes until now may be missing", at(2))
	tl.Observe(deployment("u1", "5", "app:3", 3, nil), at(3))
	tl.Deleted(nil, at(4))
	tl.Deleted(nil, at(5)) // a second report of the same deletion
	s := tl.Snapshot()
	if got := kinds(s); !slices.Equal(got, []string{"initial", "gap", "change", "deleted"}) {
		t.Fatalf("kinds = %v", got)
	}
	// The change after the gap still compares with the last revision kc
	// saw, across the gap.
	if p, ok := s.Previous(2); !ok || p.ResourceVersion != "1" {
		t.Fatalf("previous = %+v, %v", p, ok)
	}
	if e := s.Entries[3]; e.Object != nil || !strings.Contains(e.Note, "without its final state") || e.UID != "u1" {
		t.Fatalf("deletion = %+v", e)
	}
	tl.Stop(at(6))
	if tl.Observe(deployment("u1", "9", "app:4", 4, nil), at(7)) {
		t.Fatalf("a stopped timeline recorded")
	}
	if got := tl.Snapshot(); !got.Stopped.Equal(at(6)) || len(got.Entries) != 4 {
		t.Fatalf("after stop: %+v", got)
	}
}

func TestLimitsEvictOldestFirst(t *testing.T) {
	t.Parallel()
	t.Run("entries", func(t *testing.T) {
		t.Parallel()
		tl := timeline.New(id, timeline.Limits{MaxEntries: 3}, t0)
		for i := 1; i <= 5; i++ {
			tl.Observe(deployment("u1", string(rune('a'+i)), "app:"+string(rune('0'+i)), int64(i), nil), at(i))
		}
		s := tl.Snapshot()
		if len(s.Entries) != 3 || s.Evicted != 2 || !s.EvictedUntil.Equal(at(2)) || s.Entries[0].Seq != 3 {
			t.Fatalf("entries %d, evicted %d until %v, first seq %d", len(s.Entries), s.Evicted, s.EvictedUntil, s.Entries[0].Seq)
		}
		diff, _ := s.Diff(0, timeline.PartConfig)
		if !strings.Contains(diff, "evicted by the timeline limits") {
			t.Fatalf("the oldest kept revision does not say its predecessor is gone:\n%s", diff)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		t.Parallel()
		one := timeline.New(id, timeline.Limits{}, t0)
		one.Observe(deployment("u1", "a", "app:1", 1, nil), at(1))
		size := one.Snapshot().Bytes
		tl := timeline.New(id, timeline.Limits{MaxBytes: 2*size + size/2}, t0)
		for i := 1; i <= 4; i++ {
			tl.Observe(deployment("u1", string(rune('a'+i)), "app:"+string(rune('0'+i)), int64(i), nil), at(i))
		}
		s := tl.Snapshot()
		if len(s.Entries) != 2 || s.Evicted != 2 || s.Bytes > s.Limits.MaxBytes {
			t.Fatalf("entries %d, evicted %d, bytes %d of %d", len(s.Entries), s.Evicted, s.Bytes, s.Limits.MaxBytes)
		}
	})
	t.Run("one entry always stays", func(t *testing.T) {
		t.Parallel()
		tl := timeline.New(id, timeline.Limits{MaxBytes: 1}, t0)
		tl.Observe(deployment("u1", "a", "app:1", 1, nil), at(1))
		tl.Observe(deployment("u1", "b", "app:2", 2, nil), at(2))
		if s := tl.Snapshot(); len(s.Entries) != 1 || s.Entries[0].ResourceVersion != "b" {
			t.Fatalf("entries = %+v", s.Entries)
		}
	})
}

func TestSummarizePodRestartsAndDeletion(t *testing.T) {
	t.Parallel()
	pod := func(restarts int64, deleting bool) map[string]any {
		meta := map[string]any{"name": "p"}
		if deleting {
			meta["deletionTimestamp"] = "2026-11-23T12:00:00Z"
		}
		return map[string]any{
			"metadata": meta,
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "app:1"}}},
			"status":   map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app", "restartCount": restarts}}},
		}
	}
	if got, want := timeline.Summarize(pod(1, false), pod(2, true)), []string{"restarts app 1 -> 2", "deletion requested"}; !slices.Equal(got, want) {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	label := pod(1, false)
	label["metadata"].(map[string]any)["labels"] = map[string]any{"a": "b"}
	if got := timeline.Summarize(pod(1, false), label); !slices.Equal(got, []string{"labels, annotations, owners or finalizers changed"}) {
		t.Fatalf("summary = %q", got)
	}
}
