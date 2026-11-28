package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// Deleting a namespace, a CustomResourceDefinition, a PersistentVolume or
// a node takes other objects with it. F8 shows what goes with the object
// and asks for its name to be typed (#374); see
// docs/adr/0374-delete-what-goes-with-it.md.

var (
	namespaceResource = schema.GroupResource{Resource: "namespaces"}
	crdResource       = schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}
	pvResource        = schema.GroupResource{Resource: "persistentvolumes"}
	nodeResource      = schema.GroupResource{Resource: "nodes"}
)

const (
	// deleteImpactTimeout bounds the whole count of what goes with an
	// object; impactRequestTimeout bounds each request of it.
	deleteImpactTimeout  = 15 * time.Second
	impactRequestTimeout = 5 * time.Second
	// impactPageSize is how many objects a list with a selector reads per
	// request; without a selector the API server says how many objects
	// are left, and one object is enough.
	impactPageSize = 500
	// impactMaxObjects is where a count that reads every object stops; it
	// then shows "2000+".
	impactMaxObjects = 2000
)

// takesOthersAlong reports whether deleting an object of gvr deletes other
// objects with it, so F8 asks for the object's name to be typed.
func takesOthersAlong(gvr schema.GroupVersionResource) bool {
	switch gvr.GroupResource() {
	case namespaceResource, crdResource, pvResource, nodeResource:
		return true
	}
	return false
}

// countingText is what the dialog shows until the count of what goes
// with an object of gvr arrives.
func countingText(gvr schema.GroupVersionResource) string {
	switch gvr.GroupResource() {
	case namespaceResource:
		return "Counting what is in it…"
	case crdResource:
		return "Counting its objects…"
	case pvResource:
		return "Reading the claim bound to it…"
	case nodeResource:
		return "Counting the pods on it…"
	}
	return "Counting…"
}

// deleteImpact is what goes with an object, as lines for the dialog.
type deleteImpact struct {
	lines []string
}

// deleteImpactMsg delivers the count seq started when the dialog opened.
type deleteImpactMsg struct {
	seq    uint64
	impact deleteImpact
}

// impactCounter counts what goes with target. Tests replace the real one.
type impactCounter func(ctx context.Context, target deleteTarget) deleteImpact

// startDeleteImpact makes the open delete dialog ask for target's name and
// counts what goes with target in the background. The count belongs to
// this dialog: closing it cancels the requests, and a result that arrives
// after it closed or opened for another object is dropped.
func (a *App) startDeleteImpact(target deleteTarget) tea.Cmd {
	a.stopDeleteImpact()
	a.deleteImpactSeq++
	seq := a.deleteImpactSeq
	a.deleteConfirm.RequireName(target.name, seq, countingText(target.gvr))
	count := a.countImpact
	if count == nil {
		count = countDeleteImpact
	}
	ctx, cancel := context.WithTimeout(a.ctx, deleteImpactTimeout)
	a.deleteImpactCancel = cancel
	return func() tea.Msg {
		defer cancel()
		return deleteImpactMsg{seq: seq, impact: count(ctx, target)}
	}
}

// stopDeleteImpact cancels the count of the delete dialog, if one runs,
// and makes its result stale.
func (a *App) stopDeleteImpact() {
	if a.deleteImpactCancel != nil {
		a.deleteImpactCancel()
		a.deleteImpactCancel = nil
	}
	if a.deleteConfirm != nil {
		a.deleteConfirm.forgetImpact()
	}
}

// applyDeleteImpact shows a count in the delete dialog that waits for it
// and drops any other.
func (a *App) applyDeleteImpact(msg deleteImpactMsg) {
	if a.deleteConfirm == nil || !a.deleteConfirm.SetImpact(msg.seq, msg.impact) {
		ctrllog.FromContext(a.ctx).WithName("delete").V(1).Info("dropped a stale count", "seq", msg.seq)
		return
	}
	a.deleteImpactCancel = nil
	a.layoutDeleteDialog()
	a.invalidateView("delete impact")
}

// countDeleteImpact reads what goes with target from the cluster it was
// read from: metadata lists with a limit, and gets, each with its own
// timeout. It never starts an informer.
func countDeleteImpact(ctx context.Context, target deleteTarget) deleteImpact {
	if target.cluster == nil {
		return deleteImpact{lines: []string{"kc could not count what goes with it: " + errNoInstance.Error()}}
	}
	cfg := rest.CopyConfig(target.cluster.GetConfig())
	md, err := metadata.NewForConfig(cfg)
	if err != nil {
		return deleteImpact{lines: []string{"kc could not count what goes with it: " + err.Error()}}
	}
	switch target.gvr.GroupResource() {
	case namespaceResource:
		return namespaceImpact(countNamespace(ctx, md, target))
	case crdResource:
		return crdImpact(ctx, target.cluster.Dynamic(), md, target)
	case pvResource:
		return pvImpact(ctx, target.cluster.Dynamic(), target)
	case nodeResource:
		pods := countObjects(ctx, md, target.context, podsOnNode, "", "spec.nodeName="+target.name)
		return nodeImpact(pods)
	}
	return deleteImpact{}
}

// impactKind is a kind of object kc counts, with the words that name it.
type impactKind struct {
	gvr       schema.GroupVersionResource
	one, many string
	// labelSelector lists only some objects of gvr; distinctLabel counts
	// the values of that label instead of the objects (Helm releases).
	labelSelector string
	distinctLabel string
}

var podsOnNode = impactKind{gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, one: "pod", many: "pods"}

// namespaceImpactKinds are the kinds counted in a namespace, in the order
// the dialog lists them. Everything else in the namespace is deleted too,
// uncounted. Helm keeps a release as Secrets labeled owner=helm, one per
// revision, with the release in the label name.
var namespaceImpactKinds = []impactKind{
	{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, one: "deployment", many: "deployments"},
	{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, one: "statefulset", many: "statefulsets"},
	{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, one: "daemonset", many: "daemonsets"},
	{gvr: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, one: "cronjob", many: "cronjobs"},
	{gvr: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, one: "job", many: "jobs"},
	podsOnNode,
	{gvr: schema.GroupVersionResource{Version: "v1", Resource: "services"}, one: "service", many: "services"},
	{gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, one: "ingress", many: "ingresses"},
	{gvr: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, one: "PVC", many: "PVCs"},
	{gvr: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, one: "configmap", many: "configmaps"},
	{gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, one: "secret", many: "secrets"},
	{gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, one: "Helm release", many: "Helm releases", labelSelector: "owner=helm", distinctLabel: "name"},
}

// impactCount is how many objects of one kind go with the deleted object.
type impactCount struct {
	kind impactKind
	n    int
	// atLeast means there are more: the count stopped at impactMaxObjects.
	atLeast bool
	err     error // the kind could not be counted
}

// text is "5 deployments", "2000+ pods" or "1 PVC".
func (c impactCount) text() string {
	noun := c.kind.many
	if c.n == 1 && !c.atLeast {
		noun = c.kind.one
	}
	if c.atLeast {
		return fmt.Sprintf("%d+ %s", c.n, noun)
	}
	return fmt.Sprintf("%d %s", c.n, noun)
}

// countNamespace counts each kind of namespaceImpactKinds in target, all
// at once: a dozen small requests.
func countNamespace(ctx context.Context, md metadata.Interface, target deleteTarget) []impactCount {
	counts := make([]impactCount, len(namespaceImpactKinds))
	var wg sync.WaitGroup
	for i, k := range namespaceImpactKinds {
		wg.Go(func() {
			counts[i] = countObjects(ctx, md, target.context, k, target.name, "")
		})
	}
	wg.Wait()
	return counts
}

// countObjects counts the objects of k in namespace ("" for all) that
// match fieldSelector. Without a selector one object and the API server's
// remainingItemCount give the count; otherwise kc reads metadata page by
// page up to impactMaxObjects.
func countObjects(ctx context.Context, md metadata.Interface, kubeContext string, k impactKind, namespace, fieldSelector string) impactCount {
	log := ctrllog.FromContext(ctx).WithName("delete")
	var res metadata.ResourceInterface = md.Resource(k.gvr)
	if namespace != "" {
		res = md.Resource(k.gvr).Namespace(namespace)
	}
	opts := metav1.ListOptions{LabelSelector: k.labelSelector, FieldSelector: fieldSelector, Limit: impactPageSize}
	exactRemaining := opts.LabelSelector == "" && opts.FieldSelector == "" && k.distinctLabel == ""
	if exactRemaining {
		opts.Limit = 1
	}
	out := impactCount{kind: k}
	distinct := make(map[string]struct{})
	for {
		reqCtx, cancel := context.WithTimeout(ctx, impactRequestTimeout)
		log.Info("count objects", "context", kubeContext, "gvr", k.gvr.String(), "namespace", namespace,
			"labelSelector", opts.LabelSelector, "fieldSelector", opts.FieldSelector, "limit", opts.Limit, "metadataOnly", true)
		list, err := res.List(reqCtx, opts)
		cancel()
		if err != nil {
			log.Error(err, "count objects failed", "context", kubeContext, "gvr", k.gvr.String(), "namespace", namespace)
			return impactCount{kind: k, err: err}
		}
		out.n += len(list.Items)
		for _, item := range list.Items {
			if v := item.GetLabels()[k.distinctLabel]; k.distinctLabel != "" && v != "" {
				distinct[v] = struct{}{}
			}
		}
		if list.Continue == "" {
			break
		}
		if exactRemaining && list.RemainingItemCount != nil {
			out.n += int(*list.RemainingItemCount)
			break
		}
		if out.n >= impactMaxObjects {
			out.atLeast = true
			break
		}
		opts.Continue, opts.Limit = list.Continue, impactPageSize
	}
	if k.distinctLabel != "" {
		out.n = len(distinct)
	}
	return out
}

// namespaceImpact says what a namespace holds of the counted kinds, which
// kinds were counted, and which could not be.
func namespaceImpact(counts []impactCount) deleteImpact {
	var held, counted []string
	failed := make(map[string][]string) // reason -> kinds
	var reasons []string
	for _, c := range counts {
		if c.err != nil {
			reason := impactFailure(c.err)
			if _, ok := failed[reason]; !ok {
				reasons = append(reasons, reason)
			}
			failed[reason] = append(failed[reason], c.kind.many)
			continue
		}
		counted = append(counted, c.kind.many)
		if c.n > 0 {
			held = append(held, c.text())
		}
	}
	var lines []string
	switch {
	case len(counted) == 0:
		lines = append(lines, "kc could not count what is in it; everything in it is deleted.")
	case len(held) == 0:
		lines = append(lines, "It holds none of the kinds kc counted.")
	default:
		lines = append(lines, "It holds "+joinAnd(held)+".")
	}
	for _, reason := range reasons {
		lines = append(lines, fmt.Sprintf("Not counted: %s (%s).", strings.Join(failed[reason], ", "), reason))
	}
	if len(counted) > 0 {
		lines = append(lines, "Counted: "+strings.Join(counted, ", ")+". Other kinds are not counted; everything in it is deleted.")
	}
	return deleteImpact{lines: lines}
}

// crdImpact counts the objects of the CustomResourceDefinition target in
// all namespaces: one get of the definition, one list of its objects.
func crdImpact(ctx context.Context, dyn dynamic.Interface, md metadata.Interface, target deleteTarget) deleteImpact {
	crd, err := getImpactObject(ctx, dyn, target)
	if err != nil {
		return deleteImpact{lines: []string{fmt.Sprintf("kc could not count its objects (%s); all of them are deleted.", impactFailure(err))}}
	}
	group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
	names, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "plural")
	resource := names + "." + group
	version := servedVersion(crd)
	if version == "" || names == "" {
		return deleteImpact{lines: []string{fmt.Sprintf("No version of %s is served, so kc cannot count its objects; all of them are deleted.", resource)}}
	}
	k := impactKind{gvr: schema.GroupVersionResource{Group: group, Version: version, Resource: names}, one: resource + " object", many: resource + " objects"}
	c := countObjects(ctx, md, target.context, k, "", "")
	switch {
	case c.err != nil:
		return deleteImpact{lines: []string{fmt.Sprintf("kc could not count the %s objects (%s); all of them are deleted.", resource, impactFailure(c.err))}}
	case c.n == 0:
		return deleteImpact{lines: []string{fmt.Sprintf("There are no %s objects.", resource)}}
	}
	where := "in the cluster"
	if scope, _, _ := unstructured.NestedString(crd.Object, "spec", "scope"); scope == "Namespaced" {
		where = "in all namespaces"
	}
	return deleteImpact{lines: []string{fmt.Sprintf("%s %s %s deleted with it.", c.text(), where, plural(c.n, "is", "are"))}}
}

// servedVersion is the version a list of crd's objects uses: the storage
// version when it is served, else the first served one, else "".
func servedVersion(crd *unstructured.Unstructured) string {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	first := ""
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "name")
		served, _, _ := unstructured.NestedBool(m, "served")
		storage, _, _ := unstructured.NestedBool(m, "storage")
		if !served || name == "" {
			continue
		}
		if storage {
			return name
		}
		if first == "" {
			first = name
		}
	}
	return first
}

// pvImpact names the claim bound to the PersistentVolume target and its
// reclaim policy, which decides whether the storage goes too.
func pvImpact(ctx context.Context, dyn dynamic.Interface, target deleteTarget) deleteImpact {
	pv, err := getImpactObject(ctx, dyn, target)
	if err != nil {
		return deleteImpact{lines: []string{fmt.Sprintf("kc could not read the claim bound to it (%s).", impactFailure(err))}}
	}
	claimNS, _, _ := unstructured.NestedString(pv.Object, "spec", "claimRef", "namespace")
	claim, _, _ := unstructured.NestedString(pv.Object, "spec", "claimRef", "name")
	phase, _, _ := unstructured.NestedString(pv.Object, "status", "phase")
	policy, _, _ := unstructured.NestedString(pv.Object, "spec", "persistentVolumeReclaimPolicy")
	var lines []string
	if claim != "" {
		lines = append(lines, fmt.Sprintf("Claim %s is bound to it (%s) and loses its volume.", objectRef(claimNS, claim), phaseText(phase)))
	} else {
		lines = append(lines, fmt.Sprintf("No claim is bound to it (%s).", phaseText(phase)))
	}
	switch policy {
	case "Delete":
		lines = append(lines, "Reclaim policy Delete: the storage is deleted too.")
	case "":
	default:
		lines = append(lines, fmt.Sprintf("Reclaim policy %s: the storage stays.", policy))
	}
	return deleteImpact{lines: lines}
}

func phaseText(phase string) string {
	if phase == "" {
		return "phase unknown"
	}
	return "phase " + phase
}

// nodeImpact says how many pods are bound to the node; Kubernetes deletes
// them once the node is gone.
func nodeImpact(pods impactCount) deleteImpact {
	switch {
	case pods.err != nil:
		return deleteImpact{lines: []string{fmt.Sprintf("kc could not count the pods on it (%s); they are deleted with it.", impactFailure(pods.err))}}
	case pods.n == 0:
		return deleteImpact{lines: []string{"No pods are bound to it."}}
	}
	return deleteImpact{lines: []string{fmt.Sprintf("%s bound to it %s deleted with it.", pods.text(), plural(pods.n, "is", "are"))}}
}

// getImpactObject reads target from its cluster, directly.
func getImpactObject(ctx context.Context, dyn dynamic.Interface, target deleteTarget) (*unstructured.Unstructured, error) {
	if dyn == nil {
		return nil, errors.New("no client")
	}
	reqCtx, cancel := context.WithTimeout(ctx, impactRequestTimeout)
	defer cancel()
	log := ctrllog.FromContext(ctx).WithName("delete")
	log.Info("get object", "context", target.context, "gvr", target.gvr.String(), "name", target.name)
	obj, err := dyn.Resource(target.gvr).Get(reqCtx, target.name, metav1.GetOptions{})
	if err != nil {
		log.Error(err, "get object failed", "context", target.context, "gvr", target.gvr.String(), "name", target.name)
	}
	return obj, err
}

// impactFailure says in a word or two why a count failed; the log has the
// whole error.
func impactFailure(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "forbidden"
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case apierrors.IsNotFound(err):
		return "not found"
	}
	return "failed, see the log"
}

// joinAnd joins items as "a, b and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
