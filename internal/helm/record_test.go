package helm_test

import (
	"strings"
	"testing"

	"github.com/sttts/kc/internal/helm"
	"github.com/sttts/kc/internal/helm/helmtest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRecordFromSecret(t *testing.T) {
	t.Parallel()
	base := helmtest.Release{Namespace: "apps", Name: "web", Revision: 2, Status: "deployed", Chart: "web", Version: "1.0.0"}
	cases := []struct {
		name    string
		secret  func() *unstructured.Unstructured
		ok      bool
		release string
		rev     int
	}{
		{name: "helm secret", secret: base.Secret, ok: true, release: "web", rev: 2},
		{name: "not owned by helm", ok: false, secret: func() *unstructured.Unstructured {
			u := base.Secret()
			u.SetLabels(map[string]string{"name": "web"})
			return u
		}},
		{name: "wrong type", ok: false, secret: func() *unstructured.Unstructured {
			u := base.Secret()
			u.Object["type"] = "Opaque"
			return u
		}},
		{name: "labels without name and version fall back to the Secret name", ok: true, release: "web", rev: 2, secret: func() *unstructured.Unstructured {
			u := base.Secret()
			u.SetLabels(map[string]string{"owner": "helm"})
			return u
		}},
		{name: "no release name at all", ok: false, secret: func() *unstructured.Unstructured {
			u := base.Secret()
			u.SetLabels(map[string]string{"owner": "helm"})
			u.SetName("something-else")
			return u
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, ok := helm.RecordFromSecret(tc.secret())
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if rec.Release != tc.release || rec.Revision != tc.rev || rec.Namespace != "apps" {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
}

func TestRecordDecode(t *testing.T) {
	t.Parallel()
	r := helmtest.Release{Namespace: "apps", Name: "web", Revision: 4, Status: "failed", Chart: "web", Version: "2.0.0", Values: map[string]any{"a": 1}}
	rec, ok := helm.RecordFromSecret(r.Secret())
	if !ok {
		t.Fatal("not a record")
	}
	rel, err := rec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != 4 || rel.Info.Status != "failed" || rel.ChartName() != "web-2.0.0" {
		t.Fatalf("release = %+v", rel)
	}

	bad, ok := helm.RecordFromSecret(r.CorruptSecret())
	if !ok {
		t.Fatal("corrupt Secret is still a record")
	}
	if _, err := bad.Decode(); err == nil || !strings.Contains(err.Error(), "sh.helm.release.v1.web.v4") {
		t.Fatalf("corrupt decode err = %v, want it to name the Secret", err)
	}
}

func TestGroup(t *testing.T) {
	t.Parallel()
	var recs []helm.Record
	for _, r := range []helmtest.Release{
		{Namespace: "b", Name: "web", Revision: 1, Status: "superseded"},
		{Namespace: "a", Name: "web", Revision: 2, Status: "superseded"},
		{Namespace: "a", Name: "web", Revision: 10, Status: "deployed"},
		{Namespace: "a", Name: "db", Revision: 1, Status: "deployed"},
		{Namespace: "a", Name: "web", Revision: 3, Status: "superseded"},
	} {
		rec, ok := helm.RecordFromSecret(r.Secret())
		if !ok {
			t.Fatalf("fixture %v is not a record", r)
		}
		recs = append(recs, rec)
	}
	got := helm.Group(recs)
	var names []string
	for _, h := range got {
		names = append(names, h.Namespace+"/"+h.Release)
	}
	if strings.Join(names, ",") != "a/db,a/web,b/web" {
		t.Fatalf("releases = %v", names)
	}
	web := got[1]
	var revs []int
	for _, r := range web.Revisions {
		revs = append(revs, r.Revision)
	}
	if len(revs) != 3 || revs[0] != 10 || revs[1] != 3 || revs[2] != 2 {
		t.Fatalf("revisions = %v, want newest first (numeric, not lexical)", revs)
	}
	if web.Latest().Status != "deployed" {
		t.Fatalf("latest = %+v", web.Latest())
	}
	if r, ok := web.Revision(3); !ok || r.Revision != 3 {
		t.Fatalf("Revision(3) = %+v %v", r, ok)
	}
	if _, ok := web.Revision(1); ok {
		t.Fatal("Revision(1) of a/web should not exist; it is in namespace b")
	}
}
