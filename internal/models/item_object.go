package models

import (
	"context"
	"strings"

	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ManifestFunc reads an object's manifest.
type ManifestFunc func(ctx context.Context) (*unstructured.Unstructured, error)

// ObjectRow models a concrete Kubernetes object row.
type ObjectRow struct {
	*RowItem
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	viewFn    ViewContentFunc
	manifest  ManifestFunc
	ctxName   string
	verbs     []string
}

func NewObjectRow(id string, cells []string, path []string, gvr schema.GroupVersionResource, namespace, name string, style *lipgloss.Style) *ObjectRow {
	return &ObjectRow{
		RowItem:   NewRowItem(id, cells, path, style),
		gvr:       gvr,
		namespace: namespace,
		name:      name,
	}
}

func (o *ObjectRow) GVR() schema.GroupVersionResource { return o.gvr }
func (o *ObjectRow) Namespace() string                { return o.namespace }
func (o *ObjectRow) Name() string                     { return o.name }

func (o *ObjectRow) WithViewContent(fn ViewContentFunc) *ObjectRow {
	o.viewFn = fn
	return o
}

func (o *ObjectRow) ViewContent() (string, string, string, string, string, error) {
	if o.viewFn == nil {
		return "", "", "", "", "", ErrNoViewContent
	}
	return o.viewFn()
}

// WithManifest makes the row a ManifestSource: fn reads the object, which
// lives in the kubeconfig context contextName.
func (o *ObjectRow) WithManifest(contextName string, fn ManifestFunc) *ObjectRow {
	o.ctxName = contextName
	o.manifest = fn
	return o
}

// ContextName returns the kubeconfig context of the object.
func (o *ObjectRow) ContextName() string { return o.ctxName }

// Manifest reads the object. It returns ErrNoViewContent when the row has no
// reader or the object is gone.
func (o *ObjectRow) Manifest(ctx context.Context) (*unstructured.Unstructured, error) {
	if o.manifest == nil {
		return nil, ErrNoViewContent
	}
	return o.manifest(ctx)
}

// SetResourceVerbs stores the server-supported verbs for the underlying resource.
func (o *ObjectRow) SetResourceVerbs(verbs []string) {
	o.verbs = append([]string(nil), verbs...)
}

// SupportsVerb reports whether the resource exposes the given verb.
func (o *ObjectRow) SupportsVerb(verb string) bool {
	for _, v := range o.verbs {
		if strings.EqualFold(v, verb) {
			return true
		}
	}
	return false
}
