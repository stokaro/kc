package skin_test

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/skin"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestContextColorsFor(t *testing.T) {
	t.Parallel()
	cc, err := skin.CompileContextColors([]appconfig.ContextColorConfig{
		{Match: "kind-dev", Color: "green"},
		{Match: "*prod*", Color: "red"},
		{Match: "stage-?", Color: "yellow"},
		{Regex: `^gke_[a-z-]+_europe-`, Color: "#ff8800"},
		{Match: "*", Color: "blue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		context string
		want    color.Color
		ok      bool
	}{
		{context: "kind-dev", want: lipgloss.Green, ok: true},
		{context: "eu-prod-1", want: lipgloss.Red, ok: true},
		{context: "prod", want: lipgloss.Red, ok: true},
		// "*" crosses "/", which EKS ARNs contain.
		{context: "arn:aws:eks:eu-west-1:123:cluster/prod", want: lipgloss.Red, ok: true},
		{context: "stage-1", want: lipgloss.Yellow, ok: true},
		{context: "gke_acme-dev_europe-west1_c1", want: lipgloss.Color("#ff8800"), ok: true},
		// Glob metacharacters other than * and ? are literal, and a glob
		// must match the whole name.
		{context: "kind-dev2", want: lipgloss.Blue, ok: true},
		{context: "stage-10", want: lipgloss.Blue, ok: true},
		{context: "", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.context, func(t *testing.T) {
			t.Parallel()
			got, ok := cc.For(tc.context)
			if ok != tc.ok {
				t.Fatalf("For(%q) ok = %v, want %v", tc.context, ok, tc.ok)
			}
			if ok && !sameColor(got, tc.want) {
				t.Fatalf("For(%q) = %v, want %v", tc.context, got, tc.want)
			}
		})
	}
}

func TestContextColorsZeroValueMatchesNothing(t *testing.T) {
	t.Parallel()
	var cc skin.ContextColors
	if _, ok := cc.For("prod"); ok {
		t.Fatal("zero ContextColors matched")
	}
}

func TestContextColorsLiteralGlob(t *testing.T) {
	t.Parallel()
	cc, err := skin.CompileContextColors([]appconfig.ContextColorConfig{{Match: "a.b[1]", Color: "red"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cc.For("a.b[1]"); !ok {
		t.Error("literal name with regexp metacharacters did not match itself")
	}
	if _, ok := cc.For("axb1"); ok {
		t.Error("'.' and '[' must be literal in a glob")
	}
}

func TestCompileContextColorsReportsEveryBadEntry(t *testing.T) {
	t.Parallel()
	cc, err := skin.CompileContextColors([]appconfig.ContextColorConfig{
		{Match: "*prod*", Regex: "prod", Color: "red"},
		{Color: "red"},
		{Regex: "(", Color: "red"},
		{Match: "*dev*", Color: "pink"},
		{Match: "*stage*", Color: "yellow"},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"contexts[0]: set match or regex, not both",
		"contexts[1]: set match or regex",
		"contexts[2]: regex",
		`contexts[3]: color "pink"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// The valid entry still works.
	if got, ok := cc.For("eu-stage"); !ok || !sameColor(got, lipgloss.Yellow) {
		t.Fatalf("For(eu-stage) = %v, %v; the good rule should survive", got, ok)
	}
	if _, ok := cc.For("dev"); ok {
		t.Fatal("a rule with a bad color must not match")
	}
}
