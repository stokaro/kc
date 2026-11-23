package metricsources

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// metricsGroupVersion is the resource metrics API metrics-server serves.
const metricsGroupVersion = "metrics.k8s.io/v1beta1"

// Kube is the Kubernetes side of the checks: discovery and access reviews.
type Kube struct {
	Discovery discovery.DiscoveryInterface
	Reviews   authorizationv1client.SelfSubjectAccessReviewsGetter
}

// ProbeKube checks what the current usage and the cluster capacity need:
// whether metrics.k8s.io is served, and whether the user may list pod
// metrics in the scope, node metrics, nodes and pods in the scope. It
// runs one discovery request and at most four access reviews.
func ProbeKube(ctx context.Context, k Kube, namespace string) []Check {
	served := Check{Source: "metrics.k8s.io", Name: "metrics API", Needs: "current CPU and memory usage"}
	ok, err := metricsServed(ctx, k.Discovery)
	switch {
	case err != nil:
		served.State, served.Detail = Failed, "discovery: "+err.Error()
	case !ok:
		served.State, served.Detail = Missing, metricsGroupVersion+" is not served: install metrics-server"
	default:
		served.State, served.Detail = Available, metricsGroupVersion+" is served"
	}
	out := []Check{served}
	scope := "in all namespaces"
	if namespace != "" {
		scope = "in namespace " + namespace
	}
	if served.State == Available {
		out = append(out,
			reviewCheck(ctx, k, "metrics.k8s.io", "pods", namespace, "pod usage", "current pod and workload usage "+scope),
			reviewCheck(ctx, k, "metrics.k8s.io", "nodes", "", "node usage", "current node usage"))
	}
	out = append(out,
		reviewCheck(ctx, k, "", "pods", namespace, "pods", "requests, limits and owners of the pods "+scope),
		reviewCheck(ctx, k, "", "nodes", "", "nodes", "cluster and node capacity"))
	return out
}

func metricsServed(ctx context.Context, d discovery.DiscoveryInterface) (bool, error) {
	ctrllog.FromContext(ctx).WithName("metricsources").Info("discovery get", "groupVersion", metricsGroupVersion)
	_, err := d.ServerResourcesForGroupVersion(metricsGroupVersion)
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

func reviewCheck(ctx context.Context, k Kube, group, resource, namespace, name, needs string) Check {
	c := Check{Source: "Kubernetes API", Name: name, Needs: needs}
	if group != "" {
		c.Source = group
	}
	where := ""
	if namespace != "" {
		where = " -n " + namespace
	} else if resource == "pods" {
		where = " -A"
	}
	what := resource
	if group != "" {
		what += "." + group
	}
	ctrllog.FromContext(ctx).WithName("metricsources").Info("creating SelfSubjectAccessReview", "verb", "list", "group", group, "resource", resource, "namespace", namespace)
	resp, err := k.Reviews.SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
			Verb: "list", Group: group, Resource: resource, Namespace: namespace,
		}},
	}, metav1.CreateOptions{})
	switch {
	case err != nil:
		c.State, c.Detail = Failed, "access review: "+err.Error()
	case resp.Status.Allowed:
		c.State, c.Detail = Available, "list "+what+" allowed"
	default:
		c.State, c.Detail = Forbidden, "not permitted to list "+what+": kubectl auth can-i list "+what+where
	}
	return c
}

// Candidate is a Service that looks like a Prometheus-compatible server.
type Candidate struct {
	Namespace, Service, Port string
}

func (c Candidate) String() string { return c.Namespace + "/" + c.Service + ":" + c.Port }

// candidateName matches Service names of common Prometheus-compatible
// servers and their query front ends.
var candidateName = regexp.MustCompile(`prometheus|thanos-query|thanos-querier|mimir|cortex|victoria|vmselect|vmsingle|vminsert`)

// candidatePorts are the query ports of those servers.
var candidatePorts = []int32{9090, 10902, 8428, 8481, 8080, 9009}

// maxCandidateServices bounds the one list FindCandidates sends.
const maxCandidateServices = 2000

// FindCandidates lists Services in all namespaces once and returns those
// that look like a Prometheus-compatible server. It is only called when
// the user asks; a user who may not list Services in all namespaces gets a
// Forbidden error and enters the Service instead.
func FindCandidates(ctx context.Context, services corev1client.ServicesGetter) ([]Candidate, error) {
	ctrllog.FromContext(ctx).WithName("metricsources").Info("client list", "gvr", "v1/services", "namespace", "", "limit", maxCandidateServices)
	list, err := services.Services("").List(ctx, metav1.ListOptions{Limit: maxCandidateServices})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("not permitted to list Services in all namespaces; enter the Service: %w", err)
		}
		return nil, fmt.Errorf("listing Services: %w", err)
	}
	return Candidates(list.Items), nil
}

// Candidates picks the Services whose name looks like a metrics server,
// with their query port, sorted by namespace and name.
func Candidates(svcs []corev1.Service) []Candidate {
	var out []Candidate
	for _, s := range svcs {
		if !candidateName.MatchString(strings.ToLower(s.Name)) {
			continue
		}
		port := ""
		for _, p := range s.Spec.Ports {
			if slices.Contains(candidatePorts, p.Port) {
				port = strconv.Itoa(int(p.Port))
				break
			}
		}
		if port == "" {
			continue
		}
		out = append(out, Candidate{Namespace: s.Namespace, Service: s.Name, Port: port})
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.String(), b.String()) })
	return out
}
