package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/stokaro/kc/internal/models"
)

// compareFileMaxBytes bounds the desired-state file C-x C-d reads.
const compareFileMaxBytes = 4 << 20

// fileManifest is the local-file side of C-x C-d: a desired object, such
// as a manifest rendered by kustomize or helm template, compared with the
// live object under the other cursor. It is never an object of a cluster;
// only the comparison uses it.
type fileManifest struct {
	models.Item
	path string
	// live identifies the object to pick from a file of several
	// documents.
	live models.ManifestSource
}

func (f *fileManifest) GVR() schema.GroupVersionResource { return f.live.GVR() }
func (f *fileManifest) Namespace() string                { return f.live.Namespace() }
func (f *fileManifest) Name() string                     { return f.live.Name() }
func (f *fileManifest) SupportsVerb(string) bool         { return false }
func (f *fileManifest) ContextName() string              { return "local file " + f.path }

// Manifest reads the file and returns its document for the live object.
func (f *fileManifest) Manifest(context.Context) (*unstructured.Unstructured, error) {
	obj, _, err := readFileManifest(f.path, f.live.GVR(), f.live.Namespace(), f.live.Name())
	return obj, err
}

// compareSides pairs the two cursors for C-x C-d. A local file is allowed
// on one side when the other is a cluster object.
func compareSides(left, right models.Item) (models.ManifestSource, models.ManifestSource, bool) {
	l, lok := left.(models.ManifestSource)
	r, rok := right.(models.ManifestSource)
	switch {
	case lok && rok:
		return l, r, true
	case rok:
		if p, ok := localFilePath(left); ok {
			return &fileManifest{Item: left, path: p, live: r}, r, true
		}
	case lok:
		if p, ok := localFilePath(right); ok {
			return l, &fileManifest{Item: right, path: p, live: l}, true
		}
	}
	return nil, nil, false
}

func localFilePath(item models.Item) (string, bool) {
	f, ok := item.(*models.LocalFileItem)
	if !ok {
		return "", false
	}
	return f.LocalPath(), true
}

// fileRevision names the file's content for the diff header.
type fileRevision struct {
	sum      string
	modified time.Time
}

func (r fileRevision) String() string {
	return "sha256 " + r.sum[:12] + ", modified " + r.modified.UTC().Format(time.RFC3339)
}

// readFileManifest returns the one document of the file at path that is
// the object gvr namespace/name: same API group, a kind of that resource,
// same name, and the same namespace or none. No document, or several,
// is an error that says so.
func readFileManifest(path string, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, fileRevision, error) {
	data, err := models.ReadLocalTextFile(path, compareFileMaxBytes)
	if err != nil {
		return nil, fileRevision{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fileRevision{}, fmt.Errorf("stat %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	rev := fileRevision{sum: hex.EncodeToString(sum[:]), modified: st.ModTime()}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var found []*unstructured.Unstructured
	for i := 1; ; i++ {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, rev, fmt.Errorf("%s document %d: %w", path, i, err)
		}
		if doc == nil {
			continue
		}
		u := &unstructured.Unstructured{Object: doc}
		if documentIs(u, gvr, namespace, name) {
			found = append(found, u)
		}
	}
	what := gvr.Resource + " " + name
	switch len(found) {
	case 0:
		return nil, rev, fmt.Errorf("%s has no document for %s", path, what)
	case 1:
		return found[0], rev, nil
	}
	return nil, rev, fmt.Errorf("%s has %d documents for %s", path, len(found), what)
}

func documentIs(u *unstructured.Unstructured, gvr schema.GroupVersionResource, namespace, name string) bool {
	gvk := u.GroupVersionKind()
	if gvk.Group != gvr.Group || u.GetName() != name {
		return false
	}
	if ns := u.GetNamespace(); ns != "" && ns != namespace {
		return false
	}
	plural, _ := meta.UnsafeGuessKindToResource(gvk)
	return strings.EqualFold(plural.Resource, gvr.Resource)
}
