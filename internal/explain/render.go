package explain

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// FieldText renders the full documentation of one field for the viewer,
// laid out like kubectl explain: identity first, then the field, its
// constraints and its description.
func FieldText(gvk schema.GroupVersionKind, path []string, f Field) string {
	var b strings.Builder
	writeIdentity(&b, gvk)
	fmt.Fprintf(&b, "FIELD:      %s <%s>\n", strings.Join(append(append([]string(nil), path...), f.Name), "."), f.Type)
	if f.Required {
		b.WriteString("REQUIRED:   yes\n")
	}
	if f.Format != "" {
		fmt.Fprintf(&b, "FORMAT:     %s\n", f.Format)
	}
	if len(f.Enum) > 0 {
		fmt.Fprintf(&b, "ENUM:       %s\n", strings.Join(f.Enum, ", "))
	}
	if f.Default != "" {
		fmt.Fprintf(&b, "DEFAULT:    %s\n", f.Default)
	}
	if f.ListType != "" {
		line := f.ListType
		if len(f.ListMapKeys) > 0 {
			line += " (keys: " + strings.Join(f.ListMapKeys, ", ") + ")"
		}
		fmt.Fprintf(&b, "LIST TYPE:  %s\n", line)
	}
	if f.PreserveUnknown {
		b.WriteString("UNKNOWN FIELDS: preserved (the schema does not list every field)\n")
	}
	b.WriteString("\nDESCRIPTION:\n")
	writeDescription(&b, f.Description)
	if f.Node != nil {
		if fields := f.Node.Fields(); len(fields) > 0 {
			b.WriteString("\nFIELDS:\n")
			for _, child := range fields {
				req := ""
				if child.Required {
					req = " -required-"
				}
				fmt.Fprintf(&b, "  %s\t<%s>%s\n", child.Name, child.Type, req)
			}
		}
	}
	return b.String()
}

func writeIdentity(b *strings.Builder, gvk schema.GroupVersionKind) {
	if gvk.Group != "" {
		fmt.Fprintf(b, "GROUP:      %s\n", gvk.Group)
	}
	fmt.Fprintf(b, "KIND:       %s\n", gvk.Kind)
	fmt.Fprintf(b, "VERSION:    %s\n\n", gvk.Version)
}

func writeDescription(b *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		b.WriteString("    <no description>\n")
		return
	}
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// Summary returns the first sentence or line of a description, for a
// one-line table cell.
func Summary(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	return strings.TrimSpace(text)
}
