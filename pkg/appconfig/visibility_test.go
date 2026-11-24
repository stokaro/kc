package appconfig_test

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

const visibilityConfig = `resources:
  visibility:
    defaultProfile: platform
    profiles:
      work:
        mode: work
        show:
          - {group: platform.example.com, resource: databases}
        hide: []
      app:
        mode: only
        show:
          - {group: "", resource: pods}
          - {group: apps, resource: deployments}
      platform:
        mode: hide
        hide:
          - {group: coordination.k8s.io, resource: leases}
    contexts:
      production: app
      staging: work
`

var errTest = errors.New("test")

func loadVisibility(t *testing.T, data string) appconfig.ResourcesVisibilityConfig {
	t.Helper()
	cfg, err := appconfig.NewStore(writeConfig(t, data, 0o600)).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg.Resources.Visibility
}

func TestVisibilityResolvePrecedence(t *testing.T) {
	t.Parallel()
	v := loadVisibility(t, visibilityConfig)
	if errs := v.Validate(); len(errs) > 0 {
		t.Fatalf("Validate = %v", errs)
	}
	tests := []struct {
		name     string
		context  string
		override *appconfig.VisibilityOverride
		want     string
		mode     visibility.Mode
		source   appconfig.VisibilitySource
	}{
		{name: "context binding", context: "production", want: "app", mode: visibility.ModeOnly, source: appconfig.VisibilityFromContext},
		{name: "binding to work uses the saved work changes", context: "staging", want: "work", mode: visibility.ModeWork, source: appconfig.VisibilityFromContext},
		{name: "global default", context: "dev", want: "platform", mode: visibility.ModeHide, source: appconfig.VisibilityFromDefault},
		{
			name: "panel choice beats the binding", context: "production",
			override: &appconfig.VisibilityOverride{Name: "all", Profile: visibility.Profile{Mode: visibility.ModeAll}},
			want:     "all", mode: visibility.ModeAll, source: appconfig.VisibilityFromPanel,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := v.Clone()
			v.Override = tc.override
			got := v.Resolve(tc.context)
			if got.Err != nil || got.Name != tc.want || got.Profile.Mode != tc.mode || got.Source != tc.source {
				t.Fatalf("Resolve(%q) = %+v, want %s (%s) from %v", tc.context, got, tc.want, tc.mode, tc.source)
			}
		})
	}
	work := v.Resolve("staging").Profile
	if len(work.Show) != 1 || work.Show[0] != (schema.GroupResource{Group: "platform.example.com", Resource: "databases"}) {
		t.Fatalf("work profile = %+v, want the saved Show", work)
	}
}

func TestVisibilityResolveWithoutSettingsIsWork(t *testing.T) {
	t.Parallel()
	got := appconfig.Default().Resources.Visibility.Resolve("any")
	if got.Err != nil || got.Name != appconfig.VisibilityProfileWork || !got.Profile.Equal(visibility.Work()) {
		t.Fatalf("Resolve = %+v, want plain Work", got)
	}
	got = appconfig.ResourcesVisibilityConfig{}.Resolve("any")
	if got.Source != appconfig.VisibilityFromBuiltIn || got.Name != appconfig.VisibilityProfileWork {
		t.Fatalf("Resolve of an empty config = %+v, want built-in Work", got)
	}
}

// A profile that cannot be used lists Work, never every type, and says why.
func TestVisibilityBrokenProfilesListWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    appconfig.ResourcesVisibilityConfig
	}{
		{name: "missing default", v: appconfig.ResourcesVisibilityConfig{DefaultProfile: "nope"}},
		{name: "missing binding", v: appconfig.ResourcesVisibilityConfig{Contexts: map[string]string{"dev": "nope"}}},
		{name: "only profile with a hide list", v: appconfig.ResourcesVisibilityConfig{
			DefaultProfile: "app",
			Profiles: map[string]appconfig.VisibilityProfileConfig{"app": {
				Mode: "only",
				Show: []appconfig.TypeSelector{{Resource: "pods"}},
				Hide: []appconfig.TypeSelector{{Resource: "secrets"}},
			}},
		}},
		{name: "only profile with a bad selector", v: appconfig.ResourcesVisibilityConfig{
			DefaultProfile: "app",
			Profiles:       map[string]appconfig.VisibilityProfileConfig{"app": {Mode: "only", Show: []appconfig.TypeSelector{{Resource: "Pods*"}}}},
		}},
		{name: "profile without a mode", v: appconfig.ResourcesVisibilityConfig{
			DefaultProfile: "app",
			Profiles:       map[string]appconfig.VisibilityProfileConfig{"app": {Show: []appconfig.TypeSelector{{Resource: "pods"}}}},
		}},
		{name: "work redefined as hide", v: appconfig.ResourcesVisibilityConfig{
			Profiles: map[string]appconfig.VisibilityProfileConfig{"work": {Mode: "hide"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.v.Resolve("dev")
			if got.Err == nil || !got.Profile.Equal(visibility.Work()) || got.Title() != "Work" {
				t.Fatalf("Resolve = %+v, want Work with an error", got)
			}
			if len(tc.v.Validate()) == 0 {
				t.Fatalf("Validate found nothing")
			}
		})
	}
}

func TestVisibilityValidate(t *testing.T) {
	t.Parallel()
	v := appconfig.ResourcesVisibilityConfig{
		DefaultProfile: "missing",
		Profiles: map[string]appconfig.VisibilityProfileConfig{
			"all":      {Mode: "all"},
			"Bad Name": {Mode: "all"},
			"mixed":    {Mode: "work", Show: []appconfig.TypeSelector{{Resource: "pods"}}, Hide: []appconfig.TypeSelector{{Resource: "pods"}}},
			"odd":      {Mode: "sometimes"},
		},
		Contexts: map[string]string{"prod": "gone", "dev": "mixed"},
	}
	errs := v.Validate()
	var text []string
	for _, err := range errs {
		text = append(text, err.Error())
	}
	joined := strings.Join(text, "\n")
	for _, want := range []string{
		"profiles.all: all is built in",
		`profile name "Bad Name"`,
		"profiles.mixed: pods is both shown and hidden",
		`profiles.odd: unknown mode "sometimes"`,
		`defaultProfile: profile "missing" is not defined`,
		`contexts.prod: profile "gone" is not defined`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Validate lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "contexts.dev") {
		t.Errorf("a binding to a defined profile is reported:\n%s", joined)
	}
}

// Panels get their own copy of the settings: changing one copy's profiles,
// bindings or override must not reach another.
func TestVisibilityCloneSharesNothing(t *testing.T) {
	t.Parallel()
	v := loadVisibility(t, visibilityConfig)
	v.Override = &appconfig.VisibilityOverride{Name: "app", Profile: visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{{Resource: "pods"}}}}
	c := v.Clone()
	c.Profiles["app"].Show[0].Resource = "secrets"
	c.Profiles["new"] = appconfig.VisibilityProfileConfig{Mode: "all"}
	c.Contexts["dev"] = "new"
	c.Override.Profile.Show[0].Resource = "secrets"
	if v.Profiles["app"].Show[0].Resource != "pods" || len(v.Profiles) != 3 || len(v.Contexts) != 2 || v.Override.Profile.Show[0].Resource != "pods" {
		t.Fatalf("the clone shares state: %+v", v)
	}
}

func TestVisibilityTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		e    appconfig.EffectiveVisibility
		want string
	}{
		{e: appconfig.EffectiveVisibility{Name: "work"}, want: "Work"},
		{e: appconfig.EffectiveVisibility{Name: "all"}, want: "All"},
		{e: appconfig.EffectiveVisibility{Name: "app"}, want: "app"},
		{e: appconfig.EffectiveVisibility{Name: "work", Edited: true}, want: "Work*"},
		{e: appconfig.EffectiveVisibility{Name: "app", Err: errTest}, want: "Work"},
	}
	for _, tc := range tests {
		if got := tc.e.Title(); got != tc.want {
			t.Errorf("Title(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

// A saved profile replaces its lists in the file: a list emptied in kc must
// not keep its old entries through the key-by-key merge.
func TestVisibilitySaveReplacesLists(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, visibilityConfig, 0o600)
	store := appconfig.NewStore(p)
	if _, err := store.Update(func(c *appconfig.Config) {
		c.Resources.Visibility.Profiles["platform"] = appconfig.ProfileConfig(visibility.Profile{Mode: visibility.ModeHide})
		c.Resources.Visibility.Contexts["dev"] = "platform"
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	v := cfg.Resources.Visibility
	if got := v.Profiles["platform"]; got.Mode != "hide" || len(got.Hide) != 0 {
		t.Fatalf("platform = %+v, want an empty hide list", got)
	}
	if v.Contexts["dev"] != "platform" || v.Contexts["production"] != "app" || len(v.Profiles["app"].Show) != 2 {
		t.Fatalf("other settings changed: %+v", v)
	}
	if file := string(readFile(t, p)); strings.Contains(file, "show: null") || strings.Contains(file, "hide: null") {
		t.Fatalf("the file has null lists:\n%s", file)
	}
}

func TestProfileConfigSortsSelectors(t *testing.T) {
	t.Parallel()
	got := appconfig.ProfileConfig(visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{
		{Group: "apps", Resource: "deployments"}, {Resource: "pods"}, {Group: "apps", Resource: "deployments"},
	}})
	want := []appconfig.TypeSelector{{Resource: "pods"}, {Group: "apps", Resource: "deployments"}}
	if got.Mode != "only" || len(got.Show) != 2 || got.Show[0] != want[0] || got.Show[1] != want[1] || got.Hide == nil {
		t.Fatalf("ProfileConfig = %+v, want %+v and an empty hide list", got, want)
	}
}
