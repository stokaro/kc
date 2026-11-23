package models

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var (
	secretGVR         = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	pvcGVR            = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	pvGVR             = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	serviceAccountGVR = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	serviceGVR        = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	ingressGVR        = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
)

// RelatedRef is one object in an owners or used-by folder.
type RelatedRef struct {
	// GVR is empty when an owner kind could not be resolved.
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	// Relation says why the object is listed: "controller", "owner",
	// "volume", "env", "selector", "backend" and so on.
	Relation string
	// Problem is set when the object cannot be opened: an owner kind the
	// cluster does not serve, or an owner that no longer exists.
	Problem string

	// UID is the object's UID when it is known: owner references and
	// objects kc read.
	UID types.UID
	// Owner references only.
	APIVersion string
	Kind       string

	// ownerRefs are the owner references of a listed pod, for the workload
	// rows of a used-by folder.
	ownerRefs []metav1.OwnerReference
}

// Problems of related objects that cannot be opened. They read the same in
// every related folder so a user can tell them apart.
const (
	problemNotFound  = "not found"
	problemReplaced  = "not found (an object with another UID has the name)"
	problemForbidden = "forbidden"
)

// readProblem says why reading a related object failed; empty when err is
// nil.
func readProblem(err error) string {
	switch {
	case err == nil:
		return ""
	case apierrors.IsNotFound(err):
		return problemNotFound
	case apierrors.IsForbidden(err):
		return problemForbidden
	default:
		return "unreadable: " + err.Error()
	}
}

// RelatedReader reads objects from the shared controller-runtime cache.
// *cluster.Cluster implements it.
type RelatedReader interface {
	ListByGVR(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (*unstructured.UnstructuredList, error)
	GetByGVR(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
}

// ResolveOwners maps owner references to GVRs through the RESTMapper. The
// controller reference comes first, the others keep their order. A
// namespaced owner lives in the dependent's namespace; a cluster-scoped owner
// has none.
func ResolveOwners(mapper meta.RESTMapper, namespace string, owners []metav1.OwnerReference) []RelatedRef {
	out := make([]RelatedRef, 0, len(owners))
	for _, ref := range owners {
		r := RelatedRef{
			Name:       ref.Name,
			Relation:   "owner",
			UID:        ref.UID,
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
		}
		if ref.Controller != nil && *ref.Controller {
			r.Relation = "controller"
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			r.Problem = fmt.Sprintf("invalid apiVersion %q", ref.APIVersion)
			out = append(out, r)
			continue
		}
		if mapper == nil {
			r.Problem = "no RESTMapper"
			out = append(out, r)
			continue
		}
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: ref.Kind}, gv.Version)
		if err != nil {
			r.Problem = fmt.Sprintf("unknown kind %s %s", ref.APIVersion, ref.Kind)
			out = append(out, r)
			continue
		}
		r.GVR = mapping.Resource
		if mapping.Scope != nil && mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if namespace == "" {
				// The garbage collector ignores such a reference too.
				r.Problem = "namespaced owner of a cluster-scoped object"
			}
			r.Namespace = namespace
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Relation == "controller" && out[j].Relation != "controller"
	})
	return out
}

// checkOwners marks owners that are gone. An owner with the same name but
// another UID is a new object, not the owner, as for the garbage collector.
func checkOwners(ctx context.Context, r RelatedReader, owners []RelatedRef) []RelatedRef {
	for i := range owners {
		o := &owners[i]
		if o.Problem != "" || o.GVR.Empty() {
			continue
		}
		obj, err := r.GetByGVR(ctx, o.GVR, o.Namespace, o.Name)
		switch {
		case err != nil:
			o.Problem = readProblem(err)
		case obj != nil && o.UID != "" && obj.GetUID() != o.UID:
			o.Problem = problemReplaced
		}
	}
	return owners
}

// PodReference is an object a pod spec names.
type PodReference struct {
	GVR  schema.GroupVersionResource
	Name string
	// Via is the field of the pod spec that names the object, such as
	// "volumes[cfg]", "containers[app].envFrom" or "containers[app].env[A,B]".
	Via string
}

// PodReferences lists the ConfigMaps, Secrets, PersistentVolumeClaims and
// the ServiceAccount a pod uses, through volumes (also projected and generic
// ephemeral ones), env, envFrom, imagePullSecrets and serviceAccountName.
// Each object and field appears once, in spec order.
func PodReferences(pod *corev1.Pod) []PodReference {
	if pod == nil {
		return nil
	}
	return podSpecReferences(&pod.Spec, pod.Name)
}

// podSpecReferences is PodReferences for a pod spec. podName names the
// claims of generic ephemeral volumes; a pod template has no pod name yet,
// so those claims are left out when it is empty.
func podSpecReferences(spec *corev1.PodSpec, podName string) []PodReference {
	c := podRefCollector{seen: make(map[PodReference]bool)}
	sa := spec.ServiceAccountName
	via := "serviceAccountName"
	if sa == "" {
		sa, via = spec.DeprecatedServiceAccount, "serviceAccount"
	}
	c.add(serviceAccountGVR, sa, via)
	for _, s := range spec.ImagePullSecrets {
		c.add(secretGVR, s.Name, "imagePullSecrets")
	}
	for _, v := range spec.Volumes {
		c.volume(podName, v)
	}
	for _, ct := range spec.InitContainers {
		c.env("initContainers["+ct.Name+"]", ct.Env, ct.EnvFrom)
	}
	for _, ct := range spec.Containers {
		c.env("containers["+ct.Name+"]", ct.Env, ct.EnvFrom)
	}
	for _, ct := range spec.EphemeralContainers {
		c.env("ephemeralContainers["+ct.Name+"]", ct.Env, ct.EnvFrom)
	}
	return c.out
}

// podRefCollector gathers the references of one pod in order, each once.
type podRefCollector struct {
	out  []PodReference
	seen map[PodReference]bool
}

func (c *podRefCollector) add(gvr schema.GroupVersionResource, name, via string) {
	if name == "" {
		return
	}
	r := PodReference{GVR: gvr, Name: name, Via: via}
	if c.seen[r] {
		return
	}
	c.seen[r] = true
	c.out = append(c.out, r)
}

func (c *podRefCollector) volume(podName string, v corev1.Volume) {
	field := "volumes[" + v.Name + "]"
	switch {
	case v.ConfigMap != nil:
		c.add(configMapGVR, v.ConfigMap.Name, field)
	case v.Secret != nil:
		c.add(secretGVR, v.Secret.SecretName, field)
	case v.PersistentVolumeClaim != nil:
		c.add(pvcGVR, v.PersistentVolumeClaim.ClaimName, field)
	case v.Ephemeral != nil:
		// The ephemeral volume controller names the claim <pod>-<volume>.
		if podName != "" {
			c.add(pvcGVR, podName+"-"+v.Name, field+".ephemeral")
		}
	case v.Projected != nil:
		for _, src := range v.Projected.Sources {
			if src.ConfigMap != nil {
				c.add(configMapGVR, src.ConfigMap.Name, field+".projected")
			}
			if src.Secret != nil {
				c.add(secretGVR, src.Secret.Name, field+".projected")
			}
		}
	}
}

// env adds the objects one container names in envFrom and env. The env
// variables that read one object are listed together: env[A,B].
func (c *podRefCollector) env(container string, env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
	for _, e := range envFrom {
		if e.ConfigMapRef != nil {
			c.add(configMapGVR, e.ConfigMapRef.Name, container+".envFrom")
		}
		if e.SecretRef != nil {
			c.add(secretGVR, e.SecretRef.Name, container+".envFrom")
		}
	}
	type target struct {
		gvr  schema.GroupVersionResource
		name string
	}
	var order []target
	vars := make(map[target][]string)
	note := func(t target, variable string) {
		if t.name == "" {
			return
		}
		if _, ok := vars[t]; !ok {
			order = append(order, t)
		}
		vars[t] = append(vars[t], variable)
	}
	for _, e := range env {
		if e.ValueFrom == nil {
			continue
		}
		if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
			note(target{configMapGVR, r.Name}, e.Name)
		}
		if r := e.ValueFrom.SecretKeyRef; r != nil {
			note(target{secretGVR, r.Name}, e.Name)
		}
	}
	for _, t := range order {
		c.add(t.gvr, t.name, container+".env["+strings.Join(vars[t], ",")+"]")
	}
}

// ServiceSelectsPod reports whether a Service's selector matches the pod
// labels. A Service without a selector selects nothing; its endpoints are
// managed by hand.
func ServiceSelectsPod(svc *corev1.Service, podLabels map[string]string) bool {
	if svc == nil || len(svc.Spec.Selector) == 0 {
		return false
	}
	return labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(podLabels))
}

// IngressServiceRefs returns how an Ingress names a Service: "default
// backend" or "rule <host><path>". Each way appears once.
func IngressServiceRefs(ing *networkingv1.Ingress, service string) []string {
	if ing == nil {
		return nil
	}
	var out []string
	if b := ing.Spec.DefaultBackend; b != nil && b.Service != nil && b.Service.Name == service {
		out = append(out, "default backend")
	}
	seen := make(map[string]bool)
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service == nil || p.Backend.Service.Name != service {
				continue
			}
			host := rule.Host
			if host == "" {
				host = "*"
			}
			via := "rule " + host + p.Path
			if !seen[via] {
				seen[via] = true
				out = append(out, via)
			}
		}
	}
	return out
}

// UsedByFinder finds the objects that use an object of one GVR. Sources are
// the GVRs it reads; the used-by folder watches them in the object's
// namespace (all namespaces for a cluster-scoped object) to stay current.
type UsedByFinder struct {
	Sources []schema.GroupVersionResource
	Find    func(ctx context.Context, r RelatedReader, namespace, name string) ([]RelatedRef, error)
}

var (
	usedByOnce sync.Once
	usedByMu   sync.RWMutex
	usedByMap  map[schema.GroupVersionResource]UsedByFinder
)

// loadUsedBy fills the registry with the built-in finders on first use.
func loadUsedBy() {
	usedByOnce.Do(func() { usedByMap = builtinUsedBy() })
}

// RegisterUsedBy registers how to find the users of objects of a GVR.
func RegisterUsedBy(gvr schema.GroupVersionResource, f UsedByFinder) {
	loadUsedBy()
	usedByMu.Lock()
	usedByMap[gvr] = f
	usedByMu.Unlock()
}

// UsedByFor returns the finder for a GVR when one is registered.
func UsedByFor(gvr schema.GroupVersionResource) (UsedByFinder, bool) {
	loadUsedBy()
	usedByMu.RLock()
	f, ok := usedByMap[gvr]
	usedByMu.RUnlock()
	return f, ok
}

func builtinUsedBy() map[schema.GroupVersionResource]UsedByFinder {
	m := make(map[schema.GroupVersionResource]UsedByFinder)
	for _, target := range []schema.GroupVersionResource{configMapGVR, secretGVR, pvcGVR, serviceAccountGVR} {
		m[target] = UsedByFinder{
			Sources: append([]schema.GroupVersionResource{podGVR}, templateSources...),
			Find: func(ctx context.Context, r RelatedReader, namespace, name string) ([]RelatedRef, error) {
				pods, err := podsUsing(ctx, r, target, namespace, name)
				if err != nil {
					// Without pods the templates still answer.
					partial, ok := partialRef(namespace, err)
					if !ok {
						return nil, err
					}
					pods = []RelatedRef{partial}
				}
				templates, err := templateUsers(ctx, r, target, namespace, name)
				if err != nil {
					return nil, err
				}
				return append(pods, templates...), nil
			},
		}
	}
	m[podGVR] = UsedByFinder{Sources: []schema.GroupVersionResource{serviceGVR}, Find: servicesSelecting}
	m[serviceGVR] = UsedByFinder{Sources: []schema.GroupVersionResource{ingressGVR}, Find: ingressesUsing}
	m[pvGVR] = UsedByFinder{Sources: []schema.GroupVersionResource{pvcGVR}, Find: claimsBinding}
	return m
}

func podsUsing(ctx context.Context, r RelatedReader, target schema.GroupVersionResource, namespace, name string) ([]RelatedRef, error) {
	list, err := r.ListByGVR(ctx, podGVR, namespace)
	if err != nil {
		return nil, err
	}
	var out []RelatedRef
	for i := range list.Items {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pod); err != nil {
			continue
		}
		var vias []string
		for _, ref := range PodReferences(&pod) {
			if ref.GVR == target && ref.Name == name {
				vias = append(vias, ref.Via)
			}
		}
		if len(vias) > 0 {
			out = append(out, RelatedRef{
				GVR: podGVR, Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
				Relation:  strings.Join(vias, ", "),
				ownerRefs: pod.OwnerReferences,
			})
		}
	}
	return out, nil
}

func servicesSelecting(ctx context.Context, r RelatedReader, namespace, name string) ([]RelatedRef, error) {
	pod, err := r.GetByGVR(ctx, podGVR, namespace, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	list, err := r.ListByGVR(ctx, serviceGVR, namespace)
	if err != nil {
		return nil, err
	}
	var out []RelatedRef
	for i := range list.Items {
		var svc corev1.Service
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &svc); err != nil {
			continue
		}
		if ServiceSelectsPod(&svc, pod.GetLabels()) {
			out = append(out, RelatedRef{GVR: serviceGVR, Namespace: svc.Namespace, Name: svc.Name, Relation: "selector"})
		}
	}
	return out, nil
}

func ingressesUsing(ctx context.Context, r RelatedReader, namespace, name string) ([]RelatedRef, error) {
	list, err := r.ListByGVR(ctx, ingressGVR, namespace)
	if err != nil {
		return nil, err
	}
	var out []RelatedRef
	for i := range list.Items {
		var ing networkingv1.Ingress
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &ing); err != nil {
			continue
		}
		if vias := IngressServiceRefs(&ing, name); len(vias) > 0 {
			out = append(out, RelatedRef{GVR: ingressGVR, Namespace: ing.Namespace, Name: ing.Name, Relation: strings.Join(vias, ", ")})
		}
	}
	return out, nil
}

// claimsBinding finds the claim a PersistentVolume is bound to. The volume's
// claimRef names it; the claim counts only when it names the volume back
// (spec.volumeName) and, when the claimRef has a UID, has that UID.
func claimsBinding(ctx context.Context, r RelatedReader, _, name string) ([]RelatedRef, error) {
	pv, err := r.GetByGVR(ctx, pvGVR, "", name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	ns, _, _ := unstructured.NestedString(pv.Object, "spec", "claimRef", "namespace")
	claim, _, _ := unstructured.NestedString(pv.Object, "spec", "claimRef", "name")
	uid, _, _ := unstructured.NestedString(pv.Object, "spec", "claimRef", "uid")
	if ns == "" || claim == "" {
		return nil, nil
	}
	pvc, err := r.GetByGVR(ctx, pvcGVR, ns, claim)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if vol, _, _ := unstructured.NestedString(pvc.Object, "spec", "volumeName"); vol != name {
		return nil, nil
	}
	if uid != "" && string(pvc.GetUID()) != uid {
		return nil, nil
	}
	return []RelatedRef{{GVR: pvcGVR, Namespace: ns, Name: claim, Relation: "claim"}}, nil
}
