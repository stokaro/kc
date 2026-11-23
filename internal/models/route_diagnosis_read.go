package models

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
)

var routeNamespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// isHTTPRouteGVR reports whether gvr is a served Gateway API HTTPRoute
// version.
func isHTTPRouteGVR(gvr schema.GroupVersionResource) bool {
	return gvr.Group == gatewayGroup && gvr.Resource == "httproutes"
}

// hasRouteDiagnosis reports whether objects of gvr have a routing
// diagnosis.
func hasRouteDiagnosis(gvr schema.GroupVersionResource) bool {
	return gvr == ingressGVR || isHTTPRouteGVR(gvr)
}

// routeDiagnosisWatches is what a routing diagnosis refreshes on: the
// route and the Services of its namespace, and for an HTTPRoute the
// Gateways of its namespace (in the route's API version). Parents and
// backends in other namespaces are read, not watched.
func routeDiagnosisWatches(gvr schema.GroupVersionResource, namespace string, self relatedWatch) []relatedWatch {
	watches := []relatedWatch{self, {gvr: serviceGVR, namespace: namespace}}
	if isHTTPRouteGVR(gvr) {
		gw := schema.GroupVersionResource{Group: gatewayGroup, Version: gvr.Version, Resource: "gateways"}
		watches = append(watches, relatedWatch{gvr: gw, namespace: namespace})
	}
	return watches
}

// readerRouteLookups reads a routing diagnosis's sources through the
// folder's scoped reader (shared, namespace or denied per type and
// namespace, as every related folder reads) and checks Secrets with a
// metadata-only GET.
type readerRouteLookups struct {
	ctx    context.Context
	reader RelatedReader
	mapper meta.RESTMapper
	deps   Deps
}

func (l readerRouteLookups) services(namespace string) ([]corev1.Service, error) {
	return listTyped[corev1.Service](l.ctx, l.reader, serviceGVR, namespace)
}

func (l readerRouteLookups) ingressClasses() ([]networkingv1.IngressClass, error) {
	return listTyped[networkingv1.IngressClass](l.ctx, l.reader, ingressClassGVR, "")
}

// servedGVR returns the served resource of a Gateway API kind.
func (l readerRouteLookups) servedGVR(kind string) (schema.GroupVersionResource, bool) {
	if l.mapper == nil {
		return schema.GroupVersionResource{}, false
	}
	m, err := l.mapper.RESTMapping(schema.GroupKind{Group: gatewayGroup, Kind: kind})
	if err != nil {
		return schema.GroupVersionResource{}, false
	}
	return m.Resource, true
}

func (l readerRouteLookups) gatewayGVR() (schema.GroupVersionResource, bool) {
	return l.servedGVR("Gateway")
}

func (l readerRouteLookups) gateway(namespace, name string) (*gatewayObj, error) {
	gvr, ok := l.gatewayGVR()
	if !ok {
		return nil, errTypeNotServed
	}
	obj, err := l.reader.GetByGVR(l.ctx, gvr, namespace, name)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gw := &gatewayObj{}
	if err := decodeObject(obj, gw); err != nil {
		return nil, err
	}
	return gw, nil
}

func (l readerRouteLookups) referenceGrants(namespace string) ([]referenceGrant, error) {
	gvr, ok := l.servedGVR("ReferenceGrant")
	if !ok {
		return nil, errTypeNotServed
	}
	list, err := l.reader.ListByGVR(l.ctx, gvr, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]referenceGrant, 0, len(list.Items))
	for i := range list.Items {
		var rg referenceGrant
		if err := decodeObject(&list.Items[i], &rg); err != nil {
			return nil, err
		}
		out = append(out, rg)
	}
	return out, nil
}

func (l readerRouteLookups) namespaceLabels(namespace string) (map[string]string, error) {
	obj, err := l.reader.GetByGVR(l.ctx, routeNamespacesGVR, "", namespace)
	if err != nil {
		return nil, err
	}
	return obj.GetLabels(), nil
}

// secretExists asks for the Secret's metadata only, so its data never
// reaches kc. It is a direct GET: watching Secrets to learn that one exists
// would hold every Secret of the namespace in memory.
func (l readerRouteLookups) secretExists(namespace, name string) (bool, error) {
	if l.deps.Cl == nil {
		return false, fmt.Errorf("no cluster")
	}
	client, err := metadata.NewForConfig(l.deps.Cl.GetConfig())
	if err != nil {
		return false, err
	}
	ctrllog.FromContext(l.ctx).WithName("diagnosis").Info("client get", "gvr", secretGVR.String(), "namespace", namespace, "name", name, "metadataOnly", true)
	_, err = client.Resource(secretGVR).Namespace(namespace).Get(l.ctx, name, metav1.GetOptions{})
	return notFoundAsAbsent(err)
}

// decodeObject decodes an unstructured object into one of the Gateway API
// structs above through JSON, which handles their timestamps.
func decodeObject(obj *unstructured.Unstructured, into any) error {
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

// routeDiagnosisRows reads the route and renders its diagnosis. Backend
// Services open their own Service diagnosis.
func (f *RelatedObjectsFolder) routeDiagnosisRows(ctx context.Context) ([]table.Row, error) {
	log := ctrllog.FromContext(ctx).WithName("diagnosis")
	obj, err := f.reader.GetByGVR(ctx, f.gvr, f.namespace, f.name)
	if err != nil {
		return nil, err
	}
	var mapper meta.RESTMapper
	if f.Deps.Cl != nil {
		mapper = f.Deps.Cl.RESTMapper()
	}
	look := readerRouteLookups{ctx: ctx, reader: f.reader, mapper: mapper, deps: f.Deps}
	var d diagReport
	if f.gvr == ingressGVR {
		ing := &networkingv1.Ingress{}
		if err := decodeObject(obj, ing); err != nil {
			return nil, err
		}
		d = diagnoseIngress(ing, look)
	} else {
		rt := &httpRoute{}
		if err := decodeObject(obj, rt); err != nil {
			return nil, err
		}
		d = diagnoseHTTPRoute(rt, look)
	}
	log.V(1).Info("route diagnosis", "gvr", f.gvr.String(), "namespace", f.namespace, "name", f.name, "findings", len(d.Findings), "backends", len(d.Backends))
	return f.renderRouteDiagnosis(d), nil
}

// renderRouteDiagnosis renders d like any diagnosis, except that backend
// Services enter their Service diagnosis instead of the Service's folder.
func (f *RelatedObjectsFolder) renderRouteDiagnosis(d diagReport) []table.Row {
	backends := d.Backends
	d.Backends = slices.DeleteFunc(slices.Clone(backends), func(l diagLink) bool { return l.GVR == serviceGVR })
	rows := renderDiagnosis(f.Deps, f.rowDeps, f.Path(), d)
	note := len(rows)
	if d.Note != "" {
		note-- // keep the note last
	}
	var svcRows []table.Row
	for _, l := range backends {
		if l.GVR != serviceGVR {
			continue
		}
		svcRows = append(svcRows, f.serviceBackendRow(l))
	}
	return slices.Insert(rows, note, svcRows...)
}

// serviceBackendRow is a backend Service row: Enter opens the Service's
// diagnosis, F3 its YAML.
func (f *RelatedObjectsFolder) serviceBackendRow(l diagLink) table.Row {
	ref := RelatedRef{GVR: serviceGVR, Namespace: l.Namespace, Name: l.Name, Relation: l.Evidence}
	deps := f.rowDeps(ref)
	id := "backend/" + RelatedRowID(ref)
	objPath := append(slices.Clone(f.Path()), l.Name)
	obj := NewObjectRow(id, []string{"/" + l.Name, "services", l.Namespace, l.Evidence + "; Enter: diagnosis"}, objPath, serviceGVR, l.Namespace, l.Name, WhiteStyle())
	obj.WithViewContent(objectViewContent(deps, serviceGVR, l.Namespace, l.Name))
	obj.SetViewCommand(kubectlCommand(deps, "get", "service", l.Name, "-n", l.Namespace, "-o", "yaml"))
	obj.RowItem.details = objectDetails(l.Namespace, l.Name, serviceGVR.Resource, serviceGVR.GroupVersion().String()) + ": " + l.Evidence
	return NewObjectWithChildItem(obj, func() (Folder, error) {
		return newDiagnosisFolder(deps, serviceGVR, l.Namespace, l.Name, objPath)
	})
}
