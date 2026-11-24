package visibility

import (
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WorkCatalogVersion is the version of the built-in Work set. Raise it
// when entries change, so a note can tell users what moved. A new version
// never changes a profile's own Show and Hide selections.
const WorkCatalogVersion = 1

// WorkEntry is one type of the built-in Work set.
type WorkEntry struct {
	Type schema.GroupResource
	// Why says in a few words why the type is in Work.
	Why string
	// Source is a primary reference for an entry that is not a core
	// workload type.
	Source string
	// OnlyWithout, when set, keeps the entry out of Work while that type
	// is discovered: the two are one API under two names and Work lists
	// one of them. It is set for known aliases only (Events), never
	// derived from equal Kinds.
	OnlyWithout schema.GroupResource
}

// workGroup is the catalog as written: one reason and source per group of
// related types.
type workGroup struct {
	group     string
	resources []string
	why       string
	source    string
	without   schema.GroupResource
}

// coreEvents is the Events API that kubectl get events lists.
var coreEvents = schema.GroupResource{Resource: "events"}

// workGroups is the Work set: the types a user of a cluster reads and
// changes day to day. Work is a curated working set, not a claim that
// other types are system internals. Discovery has no such flag, and API
// group, CRD categories, scope, owners, labels or verbs do not separate
// the two, so nothing outside this list joins Work on its own.
var workGroups = []workGroup{
	{
		group: "",
		resources: []string{
			"pods", "services", "configmaps", "secrets", "serviceaccounts",
			"persistentvolumeclaims", "persistentvolumes", "nodes", "namespaces",
			"events", "resourcequotas", "limitranges",
		},
		why:    "core workload, configuration, storage and cluster types",
		source: "https://kubernetes.io/docs/reference/kubernetes-api/",
	},
	{
		group:     "apps",
		resources: []string{"deployments", "statefulsets", "daemonsets"},
		why:       "workload controllers; ReplicaSets and ControllerRevisions are what they manage and stay reachable from them",
		source:    "https://kubernetes.io/docs/concepts/workloads/controllers/",
	},
	{
		group:     "batch",
		resources: []string{"jobs", "cronjobs"},
		why:       "batch workloads",
		source:    "https://kubernetes.io/docs/concepts/workloads/controllers/job/",
	},
	{
		group:     "networking.k8s.io",
		resources: []string{"ingresses", "networkpolicies"},
		why:       "traffic in and between workloads",
		source:    "https://kubernetes.io/docs/concepts/services-networking/",
	},
	{
		group:     "autoscaling",
		resources: []string{"horizontalpodautoscalers"},
		why:       "scaling of workloads",
		source:    "https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/",
	},
	{
		group:     "policy",
		resources: []string{"poddisruptionbudgets"},
		why:       "decides whether a drain or rollout may evict pods",
		source:    "https://kubernetes.io/docs/concepts/workloads/pods/disruptions/",
	},
	{
		group:     "storage.k8s.io",
		resources: []string{"storageclasses"},
		why:       "the storage a claim can ask for",
		source:    "https://kubernetes.io/docs/concepts/storage/storage-classes/",
	},
	{
		group:     "events.k8s.io",
		resources: []string{"events"},
		why:       "the newer API of the same Events; in Work only when core v1 events is not served",
		source:    "https://kubernetes.io/docs/reference/kubernetes-api/cluster-resources/event-v1/",
		without:   coreEvents,
	},
	{
		group:     "cert-manager.io",
		resources: []string{"certificates", "issuers", "clusterissuers"},
		why:       "certificates and their issuers; CertificateRequests, Orders and Challenges stay in Other but can be added",
		source:    "https://cert-manager.io/docs/concepts/certificate/",
	},
	{
		group:     "argoproj.io",
		resources: []string{"applications", "applicationsets", "appprojects"},
		why:       "Argo CD applications, including those an ApplicationSet generates",
		source:    "https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/",
	},
	{
		group:     "kustomize.toolkit.fluxcd.io",
		resources: []string{"kustomizations"},
		why:       "Flux deployments from sources",
		source:    "https://fluxcd.io/flux/components/kustomize/kustomizations/",
	},
	{
		group:     "helm.toolkit.fluxcd.io",
		resources: []string{"helmreleases"},
		why:       "Flux Helm releases; HelmCharts stay in Other but can be added",
		source:    "https://fluxcd.io/flux/components/helm/helmreleases/",
	},
	{
		group:     "source.toolkit.fluxcd.io",
		resources: []string{"gitrepositories", "ocirepositories", "helmrepositories"},
		why:       "where Flux reads what it deploys",
		source:    "https://fluxcd.io/flux/components/source/",
	},
	{
		group:     "monitoring.coreos.com",
		resources: []string{"servicemonitors", "podmonitors", "prometheusrules"},
		why:       "what applications ask Prometheus to scrape and alert on",
		source:    "https://prometheus-operator.dev/docs/getting-started/design/",
	},
	{
		group:     "gateway.networking.k8s.io",
		resources: []string{"gateways", "httproutes", "grpcroutes", "referencegrants"},
		why:       "Gateway API routing that application teams own",
		source:    "https://gateway-api.sigs.k8s.io/docs/concepts/api-overview/",
	},
	{
		group:     "cilium.io",
		resources: []string{"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies"},
		why:       "Cilium policies; endpoints, identities and IPAM records stay in Other",
		source:    "https://docs.cilium.io/en/stable/network/kubernetes/policy/",
	},
	{
		group:     "projectcalico.org",
		resources: []string{"networkpolicies", "globalnetworkpolicies"},
		why:       "the public Calico policy API; the crd.projectcalico.org backing types stay in Other",
		source:    "https://docs.tigera.io/calico/latest/reference/resources/networkpolicy",
	},
}

// workIndex maps every Work type to its entry.
var workIndex = func() map[schema.GroupResource]WorkEntry {
	idx := make(map[schema.GroupResource]WorkEntry)
	for _, g := range workGroups {
		for _, r := range g.resources {
			gr := schema.GroupResource{Group: g.group, Resource: r}
			idx[gr] = WorkEntry{Type: gr, Why: g.why, Source: g.source, OnlyWithout: g.without}
		}
	}
	return idx
}()

// WorkCatalog returns the entries of the built-in Work set, sorted by group
// and resource. The caller owns the slice.
func WorkCatalog() []WorkEntry {
	out := make([]WorkEntry, 0, len(workIndex))
	for _, e := range workIndex {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b WorkEntry) int { return compareGR(a.Type, b.Type) })
	return out
}

// WorkEntryFor returns the Work entry of gr.
func WorkEntryFor(gr schema.GroupResource) (WorkEntry, bool) {
	e, ok := workIndex[gr]
	return e, ok
}
