package helm

import (
	"bytes"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// ManifestObject is one object of a release manifest, as stored in the
// release record (not as it is in the cluster now).
type ManifestObject struct {
	APIVersion string
	Kind       string
	Name       string
	// Namespace is the namespace the manifest sets; "" when it sets none.
	Namespace string
	// Source is the template that rendered the object, from Helm's
	// "# Source:" comment.
	Source string
	// Doc is the YAML document of the object.
	Doc string
}

// ValuesYAML renders the user-supplied values as YAML.
func (r *Release) ValuesYAML() (string, error) {
	cfg := bytes.TrimSpace(r.Config)
	if len(cfg) == 0 || string(cfg) == "null" {
		return "{}\n", nil
	}
	out, err := yaml.JSONToYAML(cfg)
	if err != nil {
		return "", fmt.Errorf("release values: %w", err)
	}
	return string(out), nil
}

// HooksManifest joins the hook manifests the way "helm get hooks" prints
// them.
func (r *Release) HooksManifest() string {
	var b strings.Builder
	for _, h := range r.Hooks {
		fmt.Fprintf(&b, "---\n# Source: %s\n%s\n", h.Path, strings.TrimSpace(h.Manifest))
	}
	return b.String()
}

// SplitManifest splits a release manifest into its objects, in manifest
// order (Helm's install order). Documents that are empty or only comments
// are skipped. A v1 List is expanded into its items. A document that is
// not a Kubernetes object is reported in the error slice and skipped; the
// other objects are still returned.
func SplitManifest(manifest string) ([]ManifestObject, []error) {
	var objs []ManifestObject
	var errs []error
	for i, doc := range splitDocuments(manifest) {
		source := sourceComment(doc)
		var head struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Items []map[string]any `json:"items"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			errs = append(errs, fmt.Errorf("manifest document %d (%s): %w", i+1, sourceOr(source), err))
			continue
		}
		var probe map[string]any
		if err := yaml.Unmarshal([]byte(doc), &probe); err == nil && len(probe) == 0 {
			// Only comments or an empty mapping.
			continue
		}
		if head.Kind == "" || head.APIVersion == "" {
			errs = append(errs, fmt.Errorf("manifest document %d (%s) has no apiVersion or kind", i+1, sourceOr(source)))
			continue
		}
		if head.APIVersion == "v1" && strings.HasSuffix(head.Kind, "List") && head.Items != nil {
			for _, item := range head.Items {
				obj, err := listItem(item, source)
				if err != nil {
					errs = append(errs, fmt.Errorf("manifest document %d (%s): %w", i+1, sourceOr(source), err))
					continue
				}
				objs = append(objs, obj)
			}
			continue
		}
		objs = append(objs, ManifestObject{
			APIVersion: head.APIVersion,
			Kind:       head.Kind,
			Name:       head.Metadata.Name,
			Namespace:  head.Metadata.Namespace,
			Source:     source,
			Doc:        doc,
		})
	}
	return objs, errs
}

func listItem(item map[string]any, source string) (ManifestObject, error) {
	apiVersion, _ := item["apiVersion"].(string)
	kind, _ := item["kind"].(string)
	if apiVersion == "" || kind == "" {
		return ManifestObject{}, fmt.Errorf("list item has no apiVersion or kind")
	}
	meta, _ := item["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	doc, err := yaml.Marshal(item)
	if err != nil {
		return ManifestObject{}, err
	}
	return ManifestObject{APIVersion: apiVersion, Kind: kind, Name: name, Namespace: ns, Source: source, Doc: string(doc)}, nil
}

// splitDocuments splits YAML on "---" separator lines.
func splitDocuments(s string) []string {
	var docs []string
	var cur strings.Builder
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			docs = append(docs, strings.TrimRight(cur.String(), "\n")+"\n")
		}
		cur.Reset()
	}
	for line := range strings.Lines(s) {
		trimmed := strings.TrimRight(line, " \t\r\n")
		if trimmed == "---" || strings.HasPrefix(trimmed, "--- ") || strings.HasPrefix(trimmed, "---#") {
			flush()
			continue
		}
		cur.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			cur.WriteString("\n")
		}
	}
	flush()
	return docs
}

func sourceComment(doc string) string {
	for line := range strings.Lines(doc) {
		t := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(t, "# Source:"); ok {
			return strings.TrimSpace(rest)
		}
		if t != "" && !strings.HasPrefix(t, "#") {
			return ""
		}
	}
	return ""
}

func sourceOr(source string) string {
	if source == "" {
		return "no source"
	}
	return source
}
