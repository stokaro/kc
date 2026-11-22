package models

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// overviewTestScheme is the client-go scheme plus cert-manager's Certificate
// as an unstructured kind, so a fake client serves every overview kind.
var overviewTestScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		panic(err)
	}
	s.AddKnownTypeWithName(ovCertificateGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(listGVK(ovCertificateGVK), &unstructured.UnstructuredList{})
	return s
}()

// newOverviewFakeClient returns a fake client that lists every overview
// kind, holding objs.
func newOverviewFakeClient(objs ...crclient.Object) crclient.WithWatch {
	return fake.NewClientBuilder().WithScheme(overviewTestScheme).WithObjects(objs...).Build()
}
