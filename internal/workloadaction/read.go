package workloadaction

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

func gvkOf(gvr schema.GroupVersionResource) schema.GroupVersionKind {
	return gvr.GroupVersion().WithKind(kindOf(gvr))
}

// getObject reads ref from the shared cache into out. It reads it as
// unstructured so it shares the informer the panels and the viewer use,
// and converts it to the typed object.
func getObject(ctx context.Context, env Env, ref Ref, out any) error {
	ctrllog.FromContext(ctx).WithName("workloadaction").Info("client get",
		"gvr", ref.GVR.String(), "namespace", ref.Namespace, "name", ref.Name)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvkOf(ref.GVR))
	if err := env.Client.Get(ctx, crclient.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, u); err != nil {
		return fmt.Errorf("reading %s %s/%s: %w", kindOf(ref.GVR), ref.Namespace, ref.Name, err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out); err != nil {
		return fmt.Errorf("decoding %s %s/%s: %w", kindOf(ref.GVR), ref.Namespace, ref.Name, err)
	}
	return nil
}

// patchObject sends a raw patch to ref.
func patchObject(ctx context.Context, env Env, ref Ref, patch crclient.Patch, what string) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvkOf(ref.GVR))
	u.SetNamespace(ref.Namespace)
	u.SetName(ref.Name)
	ctrllog.FromContext(ctx).WithName("workloadaction").Info("client patch",
		"gvr", ref.GVR.String(), "namespace", ref.Namespace, "name", ref.Name, "patchType", string(patch.Type()), "change", what)
	if err := env.Client.Patch(ctx, u, patch); err != nil {
		return fmt.Errorf("patching %s %s/%s: %w", kindOf(ref.GVR), ref.Namespace, ref.Name, err)
	}
	return nil
}
