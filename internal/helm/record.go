package helm

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Record is one stored revision of a release, read from its Secret's
// labels without decoding the release.
type Record struct {
	Namespace string
	Release   string
	Revision  int
	// Status is the status label; the decoded record has the same value.
	Status string
	// Secret is the name of the Secret holding the record.
	Secret          string
	UID             string
	ResourceVersion string
	Created         time.Time
	// data is the "release" value as the Kubernetes API returns it.
	data string
}

// Key identifies the record content: it changes whenever the Secret does.
func (r Record) Key() string { return r.UID + "/" + r.ResourceVersion }

// RecordFromSecret reads a Helm release Secret. ok is false when the
// Secret is not a Helm release record.
func RecordFromSecret(u *unstructured.Unstructured) (Record, bool) {
	if u == nil {
		return Record{}, false
	}
	labels := u.GetLabels()
	if labels[OwnerLabel] != OwnerValue {
		return Record{}, false
	}
	if t, _, _ := unstructured.NestedString(u.Object, "type"); t != SecretType {
		return Record{}, false
	}
	data, _, _ := unstructured.NestedString(u.Object, "data", ReleaseKey)
	rec := Record{
		Namespace:       u.GetNamespace(),
		Release:         labels[NameLabel],
		Status:          labels[StatusLabel],
		Secret:          u.GetName(),
		UID:             string(u.GetUID()),
		ResourceVersion: u.GetResourceVersion(),
		Created:         u.GetCreationTimestamp().Time,
		data:            data,
	}
	rec.Revision = revisionOf(labels[VersionLabel], u.GetName())
	if rec.Release == "" {
		rec.Release = releaseFromSecretName(u.GetName())
	}
	if rec.Release == "" {
		return Record{}, false
	}
	return rec, true
}

// Decode decodes the release the record holds.
func (r Record) Decode() (*Release, error) {
	if r.data == "" {
		return nil, fmt.Errorf("secret %s/%s has no %q key", r.Namespace, r.Secret, ReleaseKey)
	}
	inner, err := base64.StdEncoding.DecodeString(r.data)
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s: %w", r.Namespace, r.Secret, err)
	}
	rel, err := Decode(inner)
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s: %w", r.Namespace, r.Secret, err)
	}
	return rel, nil
}

func revisionOf(label, secretName string) int {
	if v, err := strconv.Atoi(label); err == nil {
		return v
	}
	if _, after, ok := strings.CutLast(secretName, ".v"); ok {
		if v, err := strconv.Atoi(after); err == nil {
			return v
		}
	}
	return 0
}

// releaseFromSecretName parses sh.helm.release.v1.<name>.v<revision>.
func releaseFromSecretName(name string) string {
	rest, ok := strings.CutPrefix(name, "sh.helm.release.v1.")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, ".v"); i > 0 {
		return rest[:i]
	}
	return ""
}

// History is every stored revision of one release.
type History struct {
	Namespace string
	Release   string
	// Revisions are sorted newest first.
	Revisions []Record
}

// Latest returns the newest revision, which is what "helm list" shows.
func (h History) Latest() Record { return h.Revisions[0] }

// Revision returns the record of revision rev.
func (h History) Revision(rev int) (Record, bool) {
	for _, r := range h.Revisions {
		if r.Revision == rev {
			return r, true
		}
	}
	return Record{}, false
}

// Group groups records by release, sorted by namespace and name.
func Group(records []Record) []History {
	byKey := make(map[string]*History)
	for _, r := range records {
		key := r.Namespace + "/" + r.Release
		h, ok := byKey[key]
		if !ok {
			h = &History{Namespace: r.Namespace, Release: r.Release}
			byKey[key] = h
		}
		h.Revisions = append(h.Revisions, r)
	}
	out := make([]History, 0, len(byKey))
	for _, h := range byKey {
		sort.Slice(h.Revisions, func(i, j int) bool {
			a, b := h.Revisions[i], h.Revisions[j]
			if a.Revision != b.Revision {
				return a.Revision > b.Revision
			}
			return a.Secret > b.Secret
		})
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Release < out[j].Release
	})
	return out
}
