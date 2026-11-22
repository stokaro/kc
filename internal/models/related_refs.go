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
	configMapGVR      = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
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

	// Owner references only.
	UID        types.UID
	APIVersion string
	Kind       string
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
		case apierrors.IsNotFound(err):
			o.Problem = "not found"
		case err != nil:
			o.Problem = "unreadable: " + err.Error()
		case obj != nil && o.UID != "" && obj.GetUID() != o.UID:
			o.Problem = "not found (an object with another UID has the name)"
		}
	}
	return owners
}

// PodReference is an object a pod spec names.
type PodReference struct {
	GVR  schema.GroupVersionResource
	Name string
	Via  string
}

// PodReferences lists the ConfigMaps, Secrets, PersistentVolumeClaims and
// the ServiceAccount a pod uses, through volumes (also projected and generic
// ephemeral ones), env, envFrom, imagePullSecrets and serviceAccountName.
// Each object and way appears once, in spec order.
func PodReferences(pod *corev1.Pod) []PodReference {
	if pod == nil {
		return nil
	}
	var out []PodReference
	seen := map[PodReference]bool{}
	add := func(gvr schema.GroupVersionResource, name, via string) {
		if name == "" {
			return
		}
		r := PodReference{GVR: gvr, Name: name, Via: via}
		if seen[r] {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	spec := &pod.Spec
	sa := spec.ServiceAccountName
	if sa == "" {
		sa = spec.DeprecatedServiceAccount
	}
	add(serviceAccountGVR, sa, "serviceAccount")
	for _, s := range spec.ImagePullSecrets {
		add(secretGVR, s.Name, "imagePullSecrets")
	}
	for _, v := range spec.Volumes {
		switch {
		case v.ConfigMap != nil:
			add(configMapGVR, v.ConfigMap.Name, "volume")
		case v.Secret != nil:
			add(secretGVR, v.Secret.SecretName, "volume")
		case v.PersistentVolumeClaim != nil:
			add(pvcGVR, v.PersistentVolumeClaim.ClaimName, "volume")
		case v.Ephemeral != nil:
			// The ephemeral volume controller names the claim <pod>-<volume>.
			add(pvcGVR, pod.Name+"-"+v.Name, "ephemeral volume")
		case v.Projected != nil:
			for _, src := range v.Projected.Sources {
				if src.ConfigMap != nil {
					add(configMapGVR, src.ConfigMap.Name, "projected volume")
				}
				if src.Secret != nil {
					add(secretGVR, src.Secret.Name, "projected volume")
				}
			}
		}
	}
	containers := func(env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
		for _, e := range envFrom {
			if e.ConfigMapRef != nil {
				add(configMapGVR, e.ConfigMapRef.Name, "envFrom")
			}
			if e.SecretRef != nil {
				add(secretGVR, e.SecretRef.Name, "envFrom")
			}
		}
		for _, e := range env {
			if e.ValueFrom == nil {
				continue
			}
			if e.ValueFrom.ConfigMapKeyRef != nil {
				add(configMapGVR, e.ValueFrom.ConfigMapKeyRef.Name, "env")
			}
			if e.ValueFrom.SecretKeyRef != nil {
				add(secretGVR, e.ValueFrom.SecretKeyRef.Name, "env")
			}
		}
	}
	for _, c := range spec.InitContainers {
		containers(c.Env, c.EnvFrom)
	}
	for _, c := range spec.Containers {
		containers(c.Env, c.EnvFrom)
	}
	for _, c := range spec.EphemeralContainers {
		containers(c.Env, c.EnvFrom)
	}
	return out
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
	seen := map[string]bool{}
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
	usedByMu  sync.RWMutex
	usedByMap = map[schema.GroupVersionResource]UsedByFinder{}
)

// RegisterUsedBy registers how to find the users of objects of a GVR.
func RegisterUsedBy(gvr schema.GroupVersionResource, f UsedByFinder) {
	usedByMu.Lock()
	usedByMap[gvr] = f
	usedByMu.Unlock()
}

// UsedByFor returns the finder for a GVR when one is registered.
func UsedByFor(gvr schema.GroupVersionResource) (UsedByFinder, bool) {
	usedByMu.RLock()
	f, ok := usedByMap[gvr]
	usedByMu.RUnlock()
	return f, ok
}

func init() {
	for _, gvr := range []schema.GroupVersionResource{configMapGVR, secretGVR, pvcGVR, serviceAccountGVR} {
		target := gvr
		RegisterUsedBy(target, UsedByFinder{
			Sources: []schema.GroupVersionResource{podGVR},
			Find: func(ctx context.Context, r RelatedReader, namespace, name string) ([]RelatedRef, error) {
				return podsUsing(ctx, r, target, namespace, name)
			},
		})
	}
	RegisterUsedBy(podGVR, UsedByFinder{Sources: []schema.GroupVersionResource{serviceGVR}, Find: servicesSelecting})
	RegisterUsedBy(serviceGVR, UsedByFinder{Sources: []schema.GroupVersionResource{ingressGVR}, Find: ingressesUsing})
	RegisterUsedBy(pvGVR, UsedByFinder{Sources: []schema.GroupVersionResource{pvcGVR}, Find: claimsBinding})
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
			out = append(out, RelatedRef{GVR: podGVR, Namespace: pod.Namespace, Name: pod.Name, Relation: strings.Join(vias, ", ")})
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
func claimsBinding(ctx context.Context, r RelatedReader, _ string, name string) ([]RelatedRef, error) {
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
