package models

import (
	"encoding/json"
	"sync"

	"charm.land/lipgloss/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/tablecache"
)

// RowStyleInfo carries the metadata available when styling a row.
type RowStyleInfo struct {
	GVR          schema.GroupVersionResource
	GVK          schema.GroupVersionKind
	ObjectMeta   metav1.ObjectMeta
	Unstructured *unstructured.Unstructured
	BaseStyle    *lipgloss.Style
}

// RowStyler can override the base style for a given row.
type RowStyler interface {
	Style(RowStyleInfo) *lipgloss.Style
}

type rowStylerFunc func(RowStyleInfo) *lipgloss.Style

func (f rowStylerFunc) Style(info RowStyleInfo) *lipgloss.Style { return f(info) }

var (
	stylerMu        sync.RWMutex
	stylersByGVR    = make(map[schema.GroupVersionResource][]RowStyler)
	stylersByGVK    = make(map[schema.GroupVersionKind][]RowStyler)
	globalStylers   []RowStyler
	defaultStylers  sync.Once
	unstructuredDec = unstructured.UnstructuredJSONScheme
)

// RegisterRowStylerForGVR installs a styler for the exact GVR.
func RegisterRowStylerForGVR(gvr schema.GroupVersionResource, styler RowStyler) {
	stylerMu.Lock()
	defer stylerMu.Unlock()
	stylersByGVR[gvr] = append(stylersByGVR[gvr], styler)
}

// RegisterRowStylerForGVK installs a styler for the exact GVK.
func RegisterRowStylerForGVK(gvk schema.GroupVersionKind, styler RowStyler) {
	stylerMu.Lock()
	defer stylerMu.Unlock()
	stylersByGVK[gvk] = append(stylersByGVK[gvk], styler)
}

// RegisterGlobalRowStyler installs a styler invoked for every row.
func RegisterGlobalRowStyler(styler RowStyler) {
	stylerMu.Lock()
	defer stylerMu.Unlock()
	globalStylers = append(globalStylers, styler)
}

// applyRowStylers resolves all matching stylers and applies them sequentially.
func applyRowStylers(info RowStyleInfo) *lipgloss.Style {
	defaultStylers.Do(registerDefaultRowStylers)

	if info.BaseStyle == nil {
		info.BaseStyle = WhiteStyle()
	}
	style := info.BaseStyle

	stylerMu.RLock()
	defer stylerMu.RUnlock()
	seq := make([]RowStyler, 0, len(globalStylers)+len(stylersByGVR[info.GVR])+len(stylersByGVK[info.GVK]))
	seq = append(seq, globalStylers...)
	if len(info.GVR.Resource) > 0 {
		if list := stylersByGVR[info.GVR]; len(list) > 0 {
			seq = append(seq, list...)
		}
	}
	if len(info.GVK.Kind) > 0 {
		if list := stylersByGVK[info.GVK]; len(list) > 0 {
			seq = append(seq, list...)
		}
	}
	for _, styler := range seq {
		if styler == nil {
			continue
		}
		info.BaseStyle = style
		if next := styler.Style(info); next != nil {
			style = next
		}
	}
	return style
}

func registerDefaultRowStylers() {
	RegisterGlobalRowStyler(rowStylerFunc(func(info RowStyleInfo) *lipgloss.Style {
		if info.ObjectMeta.DeletionTimestamp != nil && !info.ObjectMeta.DeletionTimestamp.IsZero() {
			return DeletingStyle()
		}
		return info.BaseStyle
	}))
	registerHealthStylers()
	RegisterRowStylerForGVR(nodeGVR, rowStylerFunc(cordonedNodeStyle))
}

// cordonedNodeStyle shows a cordoned node (spec.unschedulable) like a node
// that is not ready: no new pods go there. It is a style only; the faults
// filter (C-z) does not count a cordoned node.
func cordonedNodeStyle(info RowStyleInfo) *lipgloss.Style {
	if info.Unstructured == nil {
		return info.BaseStyle
	}
	if cordoned, _, _ := unstructured.NestedBool(info.Unstructured.Object, "spec", "unschedulable"); cordoned {
		return NotReadyStyle()
	}
	return info.BaseStyle
}

// registerHealthStylers colors the rows of every resource that has a
// health rule (see row_health.go).
func registerHealthStylers() {
	styler := rowStylerFunc(func(info RowStyleInfo) *lipgloss.Style {
		return RowHealthStyle(ClassifyRowHealth(info.GVR, info.Unstructured), info.BaseStyle)
	})
	for gvr := range healthRules {
		RegisterRowStylerForGVR(gvr, styler)
	}
	// The health config is installed after the stylers are, and is keyed
	// by types kc does not know in advance.
	RegisterGlobalRowStyler(rowStylerFunc(func(info RowStyleInfo) *lipgloss.Style {
		if healthAdapterFor(info.GVR) == nil {
			return info.BaseStyle
		}
		return RowHealthStyle(ClassifyRowHealth(info.GVR, info.Unstructured), info.BaseStyle)
	}))
}

// RowHealthStyle returns the style of a row in state h: the colors object
// lists use, and base for a state without a color (ok, unknown).
func RowHealthStyle(h RowHealth, base *lipgloss.Style) *lipgloss.Style {
	switch h {
	case RowHealthNotReady:
		return NotReadyStyle()
	case RowHealthFailed:
		return JobFailedStyle()
	case RowHealthSucceeded:
		return JobSuccessStyle()
	}
	return base
}

type conditionState string

const (
	conditionTrue  conditionState = "True"
	conditionFalse conditionState = "False"
)

func readinessStatus(u *unstructured.Unstructured, condType string) conditionState {
	if u == nil {
		return ""
	}
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return ""
	}
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != condType {
			continue
		}
		if s, _ := m["status"].(string); s != "" {
			return conditionState(s)
		}
	}
	return ""
}

func unstructuredFromRow(row *tablecache.Row) *unstructured.Unstructured {
	if row == nil || len(row.Object.Raw) == 0 {
		return nil
	}
	obj, err := runtime.Decode(unstructuredDec, row.Object.Raw)
	if err != nil {
		// try generic map decoding as a fallback
		var data map[string]any
		if err := json.Unmarshal(row.Object.Raw, &data); err != nil {
			return nil
		}
		return &unstructured.Unstructured{Object: data}
	}
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u
	}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil
	}
	return &unstructured.Unstructured{Object: data}
}
