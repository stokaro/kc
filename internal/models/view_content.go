package models

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// viewReadTimeout bounds a read for the viewer. F3 runs the read on the UI
// thread (the Manifest panel mode runs it in a tea.Cmd), and it goes through
// the cache, which waits for the informer to sync;
// without a bound an informer that cannot sync would hang kc.
const viewReadTimeout = 5 * time.Second

// binaryMIME marks view content whose body is raw bytes rather than text.
// The viewer renders such a body as a summary and a hex dump; F5 still
// writes the body unchanged. It must match viewer.BinaryMIME.
const binaryMIME = "application/octet-stream"

func getForView(deps Deps, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(deps.Ctx, viewReadTimeout)
	defer cancel()
	obj, err := deps.Cl.GetByGVR(ctx, gvr, namespace, name)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("reading %s %s timed out, try again", gvr.Resource, name)
	}
	return obj, err
}

// objectManifest reads an object through the shared cache. Unlike
// getForView it takes the caller's context: the reader runs in a tea.Cmd, so
// the caller decides how long to wait.
func objectManifest(deps Deps, gvr schema.GroupVersionResource, namespace, name string) ManifestFunc {
	return func(ctx context.Context) (*unstructured.Unstructured, error) {
		obj, err := deps.Cl.GetByGVR(ctx, gvr, namespace, name)
		if err != nil {
			return nil, err
		}
		if obj == nil {
			return nil, ErrNoViewContent
		}
		return obj, nil
	}
}

func objectViewContent(deps Deps, gvr schema.GroupVersionResource, namespace, name string) ViewContentFunc {
	return func() (string, string, string, string, string, error) {
		obj, err := getForView(deps, gvr, namespace, name)
		if err != nil {
			return "", "", "", "", "", err
		}
		if obj == nil {
			return "", "", "", "", "", ErrNoViewContent
		}
		unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
		data, err := yaml.Marshal(obj.Object)
		if err != nil {
			return "", "", "", "", "", err
		}
		title := name
		if title == "" {
			title = obj.GetName()
		}
		if title == "" {
			title = gvr.Resource
		}
		return title, string(data), "yaml", "application/yaml", title + ".yaml", nil
	}
}

func keyViewContent(deps Deps, gvr schema.GroupVersionResource, namespace, name, key string, secret bool) ViewContentFunc {
	return func() (string, string, string, string, string, error) {
		obj, err := getForView(deps, gvr, namespace, name)
		if err != nil {
			return "", "", "", "", "", err
		}
		if obj == nil {
			return "", "", "", "", "", ErrNoViewContent
		}
		data, found, _ := unstructured.NestedMap(obj.Object, "data")
		title := fmt.Sprintf("%s:%s", name, key)
		filename := fmt.Sprintf("%s_%s", name, key)
		if !found {
			return title, "", "", "", filename, nil
		}
		val, ok := data[key]
		if !ok {
			return title, "", "", "", filename, nil
		}
		switch v := val.(type) {
		case string:
			if secret {
				decoded, err := base64.StdEncoding.DecodeString(v)
				if err != nil {
					return title, v, "", "", filename, nil
				}
				if isProbablyText(decoded) {
					return title, string(decoded), "", "", filename, nil
				}
				return title, string(decoded), "", binaryMIME, filename, nil
			}
			return title, v, "", "", filename, nil
		default:
			out, err := yaml.Marshal(v)
			if err != nil {
				return "", "", "", "", "", err
			}
			return title, string(out), "yaml", "application/yaml", filename + ".yaml", nil
		}
	}
}

// containerViewContent renders the spec of one container of a pod. kind
// selects the pod spec list the container lives in, so the viewer reads the
// same entry as the kubectl command shown for the row.
func containerViewContent(deps Deps, namespace, pod, container string, kind containerKind) ViewContentFunc {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	return func() (string, string, string, string, string, error) {
		obj, err := getForView(deps, gvr, namespace, pod)
		if err != nil {
			return "", "", "", "", "", err
		}
		if obj == nil {
			return "", "", "", "", "", ErrNoViewContent
		}
		m := findContainer(obj.Object, kind, container)
		if m == nil {
			return "", "", "", "", "", fmt.Errorf("%s %q not found", containerKindLabel(kind), container)
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			return "", "", "", "", "", err
		}
		title := fmt.Sprintf("%s/%s", pod, container)
		return title, string(out), "yaml", "application/yaml", container + ".yaml", nil
	}
}

// findContainer returns the entry named name in the pod spec list that kind
// selects (spec.containers, spec.initContainers or spec.ephemeralContainers).
// Every entry, ephemeral ones included, carries its name in a top-level
// "name" field; none of them has metadata of its own.
func findContainer(obj map[string]any, kind containerKind, name string) map[string]any {
	arr, found, _ := unstructured.NestedSlice(obj, "spec", containerField(kind))
	if !found {
		return nil
	}
	for _, c := range arr {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); n == name {
			return m
		}
	}
	return nil
}

func containerKindLabel(kind containerKind) string {
	switch kind {
	case containerKindInit:
		return "init container"
	case containerKindEphemeral:
		return "ephemeral container"
	default:
		return "container"
	}
}

func isProbablyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	// Invalid UTF-8, DEL and C1 controls (0x9b is CSI) would reach the
	// terminal as mangled text or control sequences.
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			continue
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			return false
		}
	}
	return true
}

func decodeConfigMap(obj map[string]any) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

func decodeSecret(obj map[string]any) (*corev1.Secret, error) {
	var sec corev1.Secret
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &sec); err != nil {
		return nil, err
	}
	return &sec, nil
}
