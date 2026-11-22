// Package helmtest builds Helm release Secrets for tests, the way Helm's
// Secret driver stores them: JSON, gzip, base64, and the base64 of Secret
// data on top.
package helmtest

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/helm"
)

// Release is a release record fixture.
type Release struct {
	Namespace   string
	Name        string
	Revision    int
	Status      string
	Chart       string
	Version     string
	AppVersion  string
	Description string
	Updated     string // RFC 3339
	Notes       string
	Values      map[string]any
	Manifest    string
}

// Record is the JSON Helm stores for r.
func (r Release) Record() map[string]any {
	updated := r.Updated
	if updated == "" {
		updated = "2026-03-04T05:06:07Z"
	}
	rec := map[string]any{
		"name":      r.Name,
		"namespace": r.Namespace,
		"version":   r.Revision,
		"info": map[string]any{
			"first_deployed": "2026-01-01T00:00:00Z",
			"last_deployed":  updated,
			"deleted":        "",
			"description":    r.Description,
			"status":         r.Status,
			"notes":          r.Notes,
		},
		"chart": map[string]any{
			"metadata": map[string]any{"name": r.Chart, "version": r.Version, "appVersion": r.AppVersion},
		},
		"manifest": r.Manifest,
	}
	if r.Values != nil {
		rec["config"] = r.Values
	}
	return rec
}

// Secret is the Secret Helm writes for r.
func (r Release) Secret() *unstructured.Unstructured {
	enc, err := helm.Encode(r.Record())
	if err != nil {
		panic(err)
	}
	return r.secretWithValue(enc)
}

// CorruptSecret is a Secret with Helm's labels whose record is not a valid
// release.
func (r Release) CorruptSecret() *unstructured.Unstructured {
	return r.secretWithValue(base64.StdEncoding.EncodeToString([]byte{0x1f, 0x8b, 0x08, 0, 1, 2, 3}))
}

func (r Release) secretWithValue(helmValue string) *unstructured.Unstructured {
	name := fmt.Sprintf("sh.helm.release.v1.%s.v%d", r.Name, r.Revision)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": r.Namespace,
			"labels": map[string]any{
				helm.OwnerLabel:   helm.OwnerValue,
				helm.NameLabel:    r.Name,
				helm.StatusLabel:  r.Status,
				helm.VersionLabel: strconv.Itoa(r.Revision),
			},
		},
		"type": helm.SecretType,
		"data": map[string]any{
			helm.ReleaseKey: base64.StdEncoding.EncodeToString([]byte(helmValue)),
		},
	}}
	u.SetUID(types.UID(r.Namespace + "-" + name))
	u.SetResourceVersion("1")
	return u
}
