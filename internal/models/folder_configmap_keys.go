package models

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// Values of the Type column in a ConfigMap data folder. They name the
// ConfigMap field a key lives in: data holds UTF-8 strings, binaryData
// holds arbitrary bytes.
const (
	configMapKeyText   = "text"
	configMapKeyBinary = "binary"
)

var configMapGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}

// ConfigMapKeysFolder lists the data and binaryData keys of a ConfigMap.
type ConfigMapKeysFolder struct {
	*BaseFolder
	Namespace string
	Name      string
}

// NewConfigMapKeysFolder constructs the ConfigMap data keys folder.
func NewConfigMapKeysFolder(deps Deps, parentPath []string, namespace, name string) *ConfigMapKeysFolder {
	path := append(slices.Clone(parentPath), "data")
	cols := []table.Column{{Title: " Name"}, {Title: " Type"}, {Title: " Size"}}
	base := NewBaseFolder(deps, cols, path)
	folder := &ConfigMapKeysFolder{BaseFolder: base, Namespace: namespace, Name: name}
	rows := newLiveKeyRowSource(deps, configMapGVR, namespace, name, folder.buildRows, folder.BaseFolder.markDirtyFromSource)
	base.SetRowSource(rows)
	return folder
}

func (f *ConfigMapKeysFolder) Parent() (gvr schema.GroupVersionResource, namespace, name string) {
	return configMapGVR, f.Namespace, f.Name
}

func (f *ConfigMapKeysFolder) buildRows(ctx context.Context) ([]table.Row, error) {
	obj, err := f.Deps.Cl.GetByGVR(ctx, configMapGVR, f.Namespace, f.Name)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, nil
	}
	cm, err := decodeConfigMap(obj.Object)
	if err != nil {
		return nil, err
	}
	entries := configMapKeyEntries(cm.Data, cm.BinaryData)
	rows := make([]table.Row, 0, len(entries))
	style := WhiteStyle()
	for _, e := range entries {
		rowPath := append(slices.Clone(f.Path()), e.key)
		item := NewSimpleItem(e.key, []string{e.key, e.kind, formatBytes(int64(e.size))}, rowPath, style)
		item.WithViewContent(configMapKeyViewContent(f.Deps, f.Namespace, f.Name, e.key))
		item.SetViewCommand(configMapKeyViewCommand(f.Deps, f.Namespace, f.Name, e.key, e.kind == configMapKeyBinary))
		rows = append(rows, item)
	}
	return rows, nil
}

// configMapKeyEntry is one row of the ConfigMap data folder.
type configMapKeyEntry struct {
	key  string
	kind string // configMapKeyText or configMapKeyBinary
	size int    // bytes of the value; decoded bytes for binaryData
}

// configMapKeyEntries merges the keys of data and binaryData, sorted by
// key. The API server refuses a key present in both fields, so the key
// alone identifies a row.
func configMapKeyEntries(data map[string]string, binaryData map[string][]byte) []configMapKeyEntry {
	entries := make([]configMapKeyEntry, 0, len(data)+len(binaryData))
	for k, v := range data {
		entries = append(entries, configMapKeyEntry{key: k, kind: configMapKeyText, size: len(v)})
	}
	for k, v := range binaryData {
		entries = append(entries, configMapKeyEntry{key: k, kind: configMapKeyBinary, size: len(v)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries
}

func configMapKeyViewContent(deps Deps, namespace, name, key string) ViewContentFunc {
	return func() (string, string, string, string, string, error) {
		obj, err := getForView(deps, configMapGVR, namespace, name)
		if err != nil {
			return "", "", "", "", "", err
		}
		if obj == nil {
			return "", "", "", "", "", ErrNoViewContent
		}
		return configMapKeyView(obj.Object, name, key)
	}
}

// configMapKeyView returns the value of one ConfigMap key for the viewer
// and for F5. The body is always the exact value: a Go string carries
// arbitrary bytes, and F5 writes the body unchanged. Binary values are
// marked with binaryMIME so the viewer shows a summary and a hex dump
// instead of writing the bytes to the terminal.
//
//revive:disable-next-line:function-result-limit returns the ViewContentFunc results, which the Viewable interface fixes
func configMapKeyView(obj map[string]any, name, key string) (title, body, lang, mime, filename string, err error) {
	title = fmt.Sprintf("%s:%s", name, key)
	filename = fmt.Sprintf("%s_%s", name, key)
	if v, found, _ := unstructured.NestedFieldNoCopy(obj, "data", key); found {
		s, ok := v.(string)
		if !ok {
			return "", "", "", "", "", fmt.Errorf("configmap %s: data key %q is not a string", name, key)
		}
		if !isProbablyText([]byte(s)) {
			return title, s, "", binaryMIME, filename, nil
		}
		return title, s, "", "", filename, nil
	}
	if v, found, _ := unstructured.NestedFieldNoCopy(obj, "binaryData", key); found {
		s, ok := v.(string)
		if !ok {
			return "", "", "", "", "", fmt.Errorf("configmap %s: binaryData key %q is not a base64 string", name, key)
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return "", "", "", "", "", fmt.Errorf("configmap %s: decoding binaryData key %q: %w", name, key, err)
		}
		return title, string(decoded), "", binaryMIME, filename, nil
	}
	return title, "", "", "", filename, nil
}
