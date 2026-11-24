package visibility_test

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/visibility"
)

// gr parses "resource.group" or "resource" (core) into a selector.
func gr(s string) schema.GroupResource {
	res, group, _ := strings.Cut(s, ".")
	return schema.GroupResource{Group: group, Resource: res}
}

func grs(names ...string) []schema.GroupResource {
	out := make([]schema.GroupResource, 0, len(names))
	for _, n := range names {
		out = append(out, gr(n))
	}
	return out
}

func names(list []schema.GroupResource) []string {
	out := make([]string, 0, len(list))
	for _, g := range list {
		out = append(out, visibility.Format(g))
	}
	slices.Sort(out)
	return out
}

// workSeed is the Work table of issue #319.
var workSeed = map[string][]string{
	"":                            {"pods", "services", "configmaps", "secrets", "serviceaccounts", "persistentvolumeclaims", "persistentvolumes", "nodes", "namespaces", "events", "resourcequotas", "limitranges"},
	"apps":                        {"deployments", "statefulsets", "daemonsets"},
	"batch":                       {"jobs", "cronjobs"},
	"networking.k8s.io":           {"ingresses", "networkpolicies"},
	"autoscaling":                 {"horizontalpodautoscalers"},
	"policy":                      {"poddisruptionbudgets"},
	"storage.k8s.io":              {"storageclasses"},
	"events.k8s.io":               {"events"},
	"cert-manager.io":             {"certificates", "issuers", "clusterissuers"},
	"argoproj.io":                 {"applications", "applicationsets", "appprojects"},
	"kustomize.toolkit.fluxcd.io": {"kustomizations"},
	"helm.toolkit.fluxcd.io":      {"helmreleases"},
	"source.toolkit.fluxcd.io":    {"gitrepositories", "ocirepositories", "helmrepositories"},
	"monitoring.coreos.com":       {"servicemonitors", "podmonitors", "prometheusrules"},
	"gateway.networking.k8s.io":   {"gateways", "httproutes", "grpcroutes", "referencegrants"},
	"cilium.io":                   {"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies"},
	"projectcalico.org":           {"networkpolicies", "globalnetworkpolicies"},
}

func TestWorkCatalogIsTheSeed(t *testing.T) {
	t.Parallel()
	var want []string
	for group, resources := range workSeed {
		for _, r := range resources {
			want = append(want, visibility.Format(schema.GroupResource{Group: group, Resource: r}))
		}
	}
	slices.Sort(want)
	catalog := visibility.WorkCatalog()
	got := make([]schema.GroupResource, 0, len(catalog))
	for _, e := range catalog {
		got = append(got, e.Type)
		if err := visibility.ValidateSelector(e.Type); err != nil {
			t.Errorf("entry %s: %v", visibility.Format(e.Type), err)
		}
		if e.Why == "" || !strings.HasPrefix(e.Source, "https://") {
			t.Errorf("entry %s has no reason or source: %+v", visibility.Format(e.Type), e)
		}
		if found, ok := visibility.WorkEntryFor(e.Type); !ok || found != e {
			t.Errorf("WorkEntryFor(%s) = %+v, %v", visibility.Format(e.Type), found, ok)
		}
	}
	if !slices.Equal(names(got), want) {
		t.Fatalf("Work catalog:\n got %v\nwant %v", names(got), want)
	}
	if visibility.WorkCatalogVersion != 1 {
		t.Fatalf("WorkCatalogVersion = %d; a change of the catalog needs a new version and this test updated", visibility.WorkCatalogVersion)
	}
}

// fixture is a discovery of a cluster with ordinary types, known operator
// APIs, implementation artifacts, unknown CRDs and both Calico layouts.
var fixture = grs(
	// Ordinary types.
	"pods", "services", "configmaps", "secrets", "events", "namespaces", "nodes",
	"deployments.apps", "replicasets.apps", "controllerrevisions.apps",
	"jobs.batch", "networkpolicies.networking.k8s.io", "endpointslices.discovery.k8s.io",
	"leases.coordination.k8s.io", "events.events.k8s.io",
	"customresourcedefinitions.apiextensions.k8s.io", "validatingwebhookconfigurations.admissionregistration.k8s.io",
	// Known operator APIs and their artifacts.
	"certificates.cert-manager.io", "certificaterequests.cert-manager.io",
	"orders.acme.cert-manager.io", "challenges.acme.cert-manager.io",
	"applications.argoproj.io", "helmreleases.helm.toolkit.fluxcd.io", "helmcharts.source.toolkit.fluxcd.io",
	"ciliumnetworkpolicies.cilium.io", "ciliumendpoints.cilium.io", "ciliumidentities.cilium.io",
	"networkpolicies.projectcalico.org", "networkpolicies.crd.projectcalico.org", "ipamblocks.crd.projectcalico.org",
	// Unknown CRDs: a business API, a cluster-scoped user API and an
	// Application of another group.
	"orders.shop.example.com", "databases.platform.example.com", "applications.app.k8s.io",
)

func TestFreshWorkSplit(t *testing.T) {
	t.Parallel()
	set, err := visibility.Evaluate(visibility.Work(), fixture)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	wantIncluded := []string{
		"applications.argoproj.io", "certificates.cert-manager.io", "ciliumnetworkpolicies.cilium.io",
		"configmaps", "deployments.apps", "events", "helmreleases.helm.toolkit.fluxcd.io",
		"jobs.batch", "namespaces", "networkpolicies.networking.k8s.io", "networkpolicies.projectcalico.org",
		"nodes", "pods", "secrets", "services",
	}
	var included []schema.GroupResource
	for _, g := range fixture {
		d, ok := set.Decide(g)
		if !ok {
			t.Fatalf("Decide(%s): not discovered", visibility.Format(g))
		}
		if d.Included {
			included = append(included, g)
			if d.Explicit || d.Reason != visibility.ReasonWork {
				t.Errorf("%s: %+v, want in Work, not explicit", visibility.Format(g), d)
			}
		}
	}
	if got := names(included); !slices.Equal(got, wantIncluded) {
		t.Fatalf("included:\n got %v\nwant %v", got, wantIncluded)
	}
	if got, want := len(set.Other()), len(fixture)-len(wantIncluded); got != want {
		t.Fatalf("Other() has %d types, want %d", got, want)
	}
	// Outside Work is "not in Work", never a system label, whatever the
	// group, owner or scope.
	for _, name := range []string{
		"certificaterequests.cert-manager.io", "helmcharts.source.toolkit.fluxcd.io",
		"orders.shop.example.com", "databases.platform.example.com", "applications.app.k8s.io",
		"networkpolicies.crd.projectcalico.org", "replicasets.apps", "leases.coordination.k8s.io",
	} {
		if d, _ := set.Decide(gr(name)); d.Included || d.Reason != visibility.ReasonNotInWork {
			t.Errorf("%s: %+v, want not in Work", name, d)
		}
	}
	if d, _ := set.Decide(gr("events.events.k8s.io")); d.Included || d.Reason != visibility.ReasonAlias {
		t.Errorf("events.events.k8s.io next to core events: %+v, want the alias left out", d)
	}
	if _, ok := set.Decide(gr("widgets.example.com")); ok {
		t.Errorf("Decide found a type that was not discovered")
	}
}

func TestModes(t *testing.T) {
	t.Parallel()
	discovered := grs("pods", "deployments.apps", "replicasets.apps", "leases.coordination.k8s.io", "orders.shop.example.com")
	// added is discovered after the profile was written.
	added := gr("widgets.new.example.com")
	tests := []struct {
		name         string
		profile      visibility.Profile
		want         []string
		explicit     []string
		addedVisible bool
	}{
		{name: "work", profile: visibility.Work(), want: []string{"deployments.apps", "pods"}},
		{
			name:     "work with show and hide",
			profile:  visibility.Profile{Mode: visibility.ModeWork, Show: grs("orders.shop.example.com"), Hide: grs("pods")},
			want:     []string{"deployments.apps", "orders.shop.example.com"},
			explicit: []string{"orders.shop.example.com"},
		},
		{name: "all", profile: visibility.Profile{Mode: visibility.ModeAll}, want: names(discovered), addedVisible: true},
		{
			name:         "hide does not inherit Work",
			profile:      visibility.Profile{Mode: visibility.ModeHide, Hide: grs("leases.coordination.k8s.io")},
			want:         []string{"deployments.apps", "orders.shop.example.com", "pods", "replicasets.apps"},
			addedVisible: true,
		},
		{
			name:     "only does not inherit Work",
			profile:  visibility.Profile{Mode: visibility.ModeOnly, Show: grs("replicasets.apps", "orders.shop.example.com")},
			want:     []string{"orders.shop.example.com", "replicasets.apps"},
			explicit: []string{"orders.shop.example.com", "replicasets.apps"},
		},
		{name: "empty only lists nothing", profile: visibility.Profile{Mode: visibility.ModeOnly}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set, err := visibility.Evaluate(tc.profile, append(slices.Clone(discovered), added))
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			var got, explicit []schema.GroupResource
			for _, g := range discovered {
				d, _ := set.Decide(g)
				if d.Included {
					got = append(got, g)
				}
				if d.Explicit {
					explicit = append(explicit, g)
				}
			}
			if !slices.Equal(names(got), tc.want) {
				t.Errorf("included = %v, want %v", names(got), tc.want)
			}
			if want := tc.explicit; !slices.Equal(names(explicit), want) {
				t.Errorf("explicit = %v, want %v", names(explicit), want)
			}
			if d, _ := set.Decide(added); d.Included != tc.addedVisible {
				t.Errorf("new type: %+v, want included %v", d, tc.addedVisible)
			}
			wantOther := len(discovered) - len(got)
			if !tc.addedVisible {
				wantOther++
			}
			if other := set.Other(); len(other) != wantOther || slices.Contains(other, added) == tc.addedVisible {
				t.Errorf("Other() = %v, want %d types", names(other), wantOther)
			}
		})
	}
}

func TestEventsAndCalicoRules(t *testing.T) {
	t.Parallel()
	core, newer := gr("events"), gr("events.events.k8s.io")
	public, backing := gr("networkpolicies.projectcalico.org"), gr("networkpolicies.crd.projectcalico.org")
	tests := []struct {
		name       string
		profile    visibility.Profile
		discovered []schema.GroupResource
		want       []string
	}{
		{name: "work prefers core events", profile: visibility.Work(), discovered: []schema.GroupResource{core, newer}, want: []string{"events"}},
		{name: "work falls back to events.k8s.io", profile: visibility.Work(), discovered: []schema.GroupResource{newer}, want: []string{"events.events.k8s.io"}},
		{
			name:       "an explicit show lists both",
			profile:    visibility.Profile{Mode: visibility.ModeWork, Show: []schema.GroupResource{newer}},
			discovered: []schema.GroupResource{core, newer}, want: []string{"events", "events.events.k8s.io"},
		},
		{
			name:       "hiding core events does not bring the alias in",
			profile:    visibility.Profile{Mode: visibility.ModeWork, Hide: []schema.GroupResource{core}},
			discovered: []schema.GroupResource{core, newer},
		},
		{name: "all lists both events", profile: visibility.Profile{Mode: visibility.ModeAll}, discovered: []schema.GroupResource{core, newer}, want: []string{"events", "events.events.k8s.io"}},
		{
			name:       "only addresses one events API",
			profile:    visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{newer}},
			discovered: []schema.GroupResource{core, newer}, want: []string{"events.events.k8s.io"},
		},
		{name: "calico backing types stay out without the public API", profile: visibility.Work(), discovered: []schema.GroupResource{backing}},
		{name: "calico public API is in work", profile: visibility.Work(), discovered: []schema.GroupResource{public, backing}, want: []string{"networkpolicies.projectcalico.org"}},
		{
			name:       "same plural in another group is another type",
			profile:    visibility.Profile{Mode: visibility.ModeHide, Hide: []schema.GroupResource{public}},
			discovered: []schema.GroupResource{public, backing, gr("networkpolicies.networking.k8s.io")},
			want:       []string{"networkpolicies.crd.projectcalico.org", "networkpolicies.networking.k8s.io"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set, err := visibility.Evaluate(tc.profile, tc.discovered)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			var got []schema.GroupResource
			for _, g := range tc.discovered {
				if d, _ := set.Decide(g); d.Included {
					got = append(got, g)
				}
			}
			if !slices.Equal(names(got), tc.want) {
				t.Fatalf("included = %v, want %v", names(got), tc.want)
			}
		})
	}
}

func TestMissingSelectorsAreKept(t *testing.T) {
	t.Parallel()
	p := visibility.Profile{Mode: visibility.ModeWork, Show: grs("databases.platform.example.com", "pods"), Hide: grs("leases.coordination.k8s.io")}
	set, err := visibility.Evaluate(p, grs("pods", "deployments.apps"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got, want := names(set.Missing()), []string{"databases.platform.example.com", "leases.coordination.k8s.io"}; !slices.Equal(got, want) {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
	// The returned slices are copies.
	set.Missing()[0] = gr("x")
	if got := names(set.Missing()); got[0] != "databases.platform.example.com" {
		t.Fatalf("Missing() shares its slice: %v", got)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		profile visibility.Profile
		wantErr string
	}{
		{name: "work", profile: visibility.Work()},
		{name: "work with lists", profile: visibility.Profile{Mode: visibility.ModeWork, Show: grs("orders.shop.example.com"), Hide: grs("pods")}},
		{name: "empty only is valid", profile: visibility.Profile{Mode: visibility.ModeOnly}},
		{name: "unknown mode", profile: visibility.Profile{Mode: "most"}, wantErr: `unknown mode "most"`},
		{name: "no mode", profile: visibility.Profile{}, wantErr: `unknown mode ""`},
		{name: "shown and hidden", profile: visibility.Profile{Mode: visibility.ModeWork, Show: grs("pods"), Hide: grs("pods")}, wantErr: "pods is both shown and hidden"},
		{name: "all with a list", profile: visibility.Profile{Mode: visibility.ModeAll, Hide: grs("pods")}, wantErr: "mode all takes no show or hide list"},
		{name: "hide with show", profile: visibility.Profile{Mode: visibility.ModeHide, Show: grs("pods")}, wantErr: "mode hide takes a hide list only"},
		{name: "only with hide", profile: visibility.Profile{Mode: visibility.ModeOnly, Hide: grs("pods")}, wantErr: "mode only takes a show list only"},
		{name: "empty resource", profile: visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{{Group: "apps"}}}, wantErr: "has no resource"},
		{name: "wildcard resource", profile: visibility.Profile{Mode: visibility.ModeOnly, Show: grs("*.apps")}, wantErr: "is not a plural resource name"},
		{name: "upper case resource", profile: visibility.Profile{Mode: visibility.ModeOnly, Show: grs("Pods")}, wantErr: "is not a plural resource name"},
		{name: "wildcard group", profile: visibility.Profile{Mode: visibility.ModeHide, Hide: []schema.GroupResource{{Group: "*.example.com", Resource: "widgets"}}}, wantErr: "is not an API group"},
		{name: "version in group", profile: visibility.Profile{Mode: visibility.ModeHide, Hide: []schema.GroupResource{{Group: "apps/v1", Resource: "deployments"}}}, wantErr: "is not an API group"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.profile.Validate()
			_, evalErr := visibility.Evaluate(tc.profile, grs("pods"))
			if tc.wantErr == "" {
				if err != nil || evalErr != nil {
					t.Fatalf("Validate() = %v, Evaluate error %v; want valid", err, evalErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want %q", err, tc.wantErr)
			}
			if evalErr == nil {
				t.Fatalf("Evaluate accepted an invalid profile")
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	for _, m := range visibility.Modes {
		if got, err := visibility.ParseMode(string(m)); err != nil || got != m {
			t.Errorf("ParseMode(%q) = %q, %v", m, got, err)
		}
	}
	if _, err := visibility.ParseMode("Work"); err == nil {
		t.Errorf("ParseMode accepted Work; modes are lower case")
	}
}

func TestCloneSharesNothing(t *testing.T) {
	t.Parallel()
	p := visibility.Profile{Mode: visibility.ModeWork, Show: grs("orders.shop.example.com"), Hide: grs("pods")}
	c := p.Clone()
	c.Show[0] = gr("x")
	c.Hide = append(c.Hide, gr("y"))
	if visibility.Format(p.Show[0]) != "orders.shop.example.com" || len(p.Hide) != 1 {
		t.Fatalf("Clone shares slices with the original: %+v", p)
	}
}

func TestReasonStrings(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for r := visibility.ReasonWork; r <= visibility.ReasonNotHidden; r++ {
		s := r.String()
		if s == "unknown" || seen[s] {
			t.Errorf("Reason(%d).String() = %q", r, s)
		}
		seen[s] = true
	}
}

func TestProfileEqual(t *testing.T) {
	t.Parallel()
	base := visibility.Profile{Mode: visibility.ModeWork, Show: grs("leases.coordination.k8s.io", "endpointslices.discovery.k8s.io"), Hide: grs("events")}
	tests := []struct {
		name  string
		other visibility.Profile
		want  bool
	}{
		{name: "same selectors in another order", other: visibility.Profile{Mode: visibility.ModeWork, Show: grs("endpointslices.discovery.k8s.io", "leases.coordination.k8s.io"), Hide: grs("events")}, want: true},
		{name: "another mode", other: visibility.Profile{Mode: visibility.ModeOnly, Show: base.Show, Hide: base.Hide}},
		{name: "a selector less", other: visibility.Profile{Mode: visibility.ModeWork, Show: grs("leases.coordination.k8s.io"), Hide: grs("events")}},
		{name: "show and hide swapped", other: visibility.Profile{Mode: visibility.ModeWork, Show: grs("events"), Hide: base.Show}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := base.Equal(tc.other); got != tc.want {
				t.Fatalf("Equal = %v, want %v", got, tc.want)
			}
		})
	}
}
