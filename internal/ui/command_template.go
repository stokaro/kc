package ui

import (
	"bytes"
	"text/template"

	"github.com/sttts/kc/internal/models"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CommandContext holds the data available to command templates
type CommandContext struct {
	// Single selection fields (also available for first item in multi-selection)
	Name      string
	Namespace string
	Kind      string
	Group     string
	Version   string
	Resource  string

	// Multi-selection fields
	Items []CommandItemContext
}

// CommandItemContext represents a single item in the selection
type CommandItemContext struct {
	Name      string
	Namespace string
	Kind      string
	Group     string
	Version   string
	Resource  string
}

// RenderCommand renders a command template with the given items. Each
// object item keeps its own name, namespace and GVR, so marked rows from
// several namespaces or resources stay distinct in .Items. resourceInfo
// is the GVR for items that carry none.
func RenderCommand(tmplStr string, items []models.Item, resourceInfo schema.GroupVersionResource) (string, error) {
	if len(items) == 0 {
		return tmplStr, nil
	}

	ctx := CommandContext{Items: commandItemContexts(items, resourceInfo)}

	// Top-level fields mirror the first item for single-item templates.
	first := ctx.Items[0]
	ctx.Name = first.Name
	ctx.Namespace = first.Namespace
	ctx.Kind = first.Kind
	ctx.Group = first.Group
	ctx.Version = first.Version
	ctx.Resource = first.Resource

	// Parse and execute template
	tmpl, err := template.New("command").Parse(tmplStr)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// commandItemContexts builds the template data for items in their order.
// Kind stays empty: object items do not expose it yet.
func commandItemContexts(items []models.Item, fallback schema.GroupVersionResource) []CommandItemContext {
	out := make([]CommandItemContext, len(items))
	for i, item := range items {
		gvr := fallback
		var c CommandItemContext
		if obj, ok := item.(models.ObjectItem); ok {
			c.Name = obj.Name()
			c.Namespace = obj.Namespace()
			if g := obj.GVR(); !g.Empty() {
				gvr = g
			}
		}
		c.Group = gvr.Group
		c.Version = gvr.Version
		c.Resource = gvr.Resource
		out[i] = c
	}
	return out
}
