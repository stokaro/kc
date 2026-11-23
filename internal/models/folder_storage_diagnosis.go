package models

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/table"
)

// maxClaimUsers bounds how many pods a claim's diagnosis explains.
const maxClaimUsers = 5

func getTyped[T any](ctx context.Context, r RelatedReader, gvr schema.GroupVersionResource, namespace, name string) (*T, error) {
	u, err := r.GetByGVR(ctx, gvr, namespace, name)
	if err != nil {
		return nil, err
	}
	var v T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v); err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", gvr.Resource, name, err)
	}
	return &v, nil
}

// storageReads collects the cluster-scoped storage objects the claims of
// some pods name, each read once through the folder's scoped reader.
type storageReads struct {
	ctx context.Context
	r   RelatedReader
	in  storageInput
}

func (s *storageReads) class(name string) {
	if _, done := s.in.Classes[name]; done || s.in.ClassErrs[name] != nil {
		return
	}
	c, err := getTyped[storagev1.StorageClass](s.ctx, s.r, storageClassesGVR, "", name)
	if err != nil {
		s.in.ClassErrs[name] = err
		return
	}
	s.in.Classes[name] = c
}

// volume reads a PV and reports whether it is a CSI volume.
func (s *storageReads) volume(name string) bool {
	if v, done := s.in.Volumes[name]; done {
		return v.Spec.CSI != nil
	}
	if s.in.VolumeErrs[name] != nil {
		return false
	}
	pv, err := getTyped[corev1.PersistentVolume](s.ctx, s.r, pvGVR, "", name)
	if err != nil {
		s.in.VolumeErrs[name] = err
		return false
	}
	s.in.Volumes[name] = pv
	if pv.Spec.CSI != nil {
		s.driver(pv.Spec.CSI.Driver)
		return true
	}
	return false
}

func (s *storageReads) driver(name string) {
	if _, done := s.in.Drivers[name]; done || s.in.DriverErrs[name] != nil {
		return
	}
	d, err := getTyped[storagev1.CSIDriver](s.ctx, s.r, csiDriversGVR, "", name)
	if err != nil {
		s.in.DriverErrs[name] = err
		return
	}
	s.in.Drivers[name] = d
}

// readStorage reads what the storage explanation of pods needs: the class
// of each unbound claim they use, the volume of each bound one, its CSI
// driver, and the VolumeAttachments when a pod on a node uses a CSI
// volume. Nothing is read for a claim the pods do not use.
func readStorage(ctx context.Context, r RelatedReader, claims []corev1.PersistentVolumeClaim, pods []*corev1.Pod) *storageInput {
	s := &storageReads{ctx: ctx, r: r, in: storageInput{
		Classes: make(map[string]*storagev1.StorageClass), ClassErrs: make(map[string]error),
		Volumes: make(map[string]*corev1.PersistentVolume), VolumeErrs: make(map[string]error),
		Drivers: make(map[string]*storagev1.CSIDriver), DriverErrs: make(map[string]error),
	}}
	needAttach := false
	for _, pod := range pods {
		names, _ := podClaims(pod)
		for _, name := range names {
			idx := slices.IndexFunc(claims, func(c corev1.PersistentVolumeClaim) bool { return c.Name == name })
			if idx < 0 {
				continue
			}
			pvc := &claims[idx]
			switch {
			case pvc.Status.Phase == corev1.ClaimBound && pvc.Spec.VolumeName != "":
				if s.volume(pvc.Spec.VolumeName) && pod.Spec.NodeName != "" {
					needAttach = true
				}
			case pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "":
				s.class(*pvc.Spec.StorageClassName)
			}
		}
	}
	if needAttach {
		s.in.Attachments, s.in.AttachmentsErr = listTyped[storagev1.VolumeAttachment](ctx, r, volumeAttachGVR, "")
		s.in.AttachmentsRead = s.in.AttachmentsErr == nil
	}
	ctrllog.FromContext(ctx).WithName("diagnosis").V(1).Info("storage inputs",
		"classes", len(s.in.Classes), "volumes", len(s.in.Volumes), "drivers", len(s.in.Drivers), "attachments", len(s.in.Attachments))
	return &s.in
}

// podStorage reads the storage input of a pod's own diagnosis.
func (f *RelatedObjectsFolder) podStorage(ctx context.Context, obj *unstructured.Unstructured, in workloadInput) *storageInput {
	if in.ClaimsErr != nil {
		return nil
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return nil
	}
	if names, _ := podClaims(&pod); len(names) == 0 {
		return nil
	}
	return readStorage(ctx, f.reader, in.Claims, []*corev1.Pod{&pod})
}

// claimInput is what a claim's diagnosis reads: the claim, the pods of its
// namespace (to find the ones that use it), the namespace's events and the
// storage objects the claim names.
type claimInput struct {
	Claim   *corev1.PersistentVolumeClaim
	Now     time.Time
	Pods    []corev1.Pod
	PodsErr error
	Events  []corev1.Event
	EvErr   error
	Storage *storageInput
}

// claimUsers returns the pods that name the claim in a volume, by name;
// the field of the first such volume goes with each.
func claimUsers(pods []corev1.Pod, claim string) (users []*corev1.Pod, fields []string) {
	for i := range pods {
		names, fs := podClaims(&pods[i])
		if j := slices.Index(names, claim); j >= 0 {
			users = append(users, &pods[i])
			fields = append(fields, fs[j])
		}
	}
	return users, fields
}

// diagnoseClaim explains a claim on its own and for each pod that uses it
// (at most maxClaimUsers): provisioning or binding, the volume, its
// attachment to each pod's node, and the volume events of pods that have
// not started.
func diagnoseClaim(in claimInput) diagReport {
	pvc := in.Claim
	d := diagReport{Note: "Facts from the cache at the read time. kc does not inspect provisioners, CSI drivers, nodes or filesystems; bound does not mean mounted."}
	d.Summary = append(d.Summary,
		fmt.Sprintf("object: persistentvolumeclaims/%s, uid %s", pvc.Name, pvc.UID),
		"read at: "+in.Now.UTC().Format(time.RFC3339),
		"claim: "+claimSummary(pvc))
	w := workloadInput{GVR: pvcGVR, Now: in.Now, Events: in.Events, EventsErr: in.EvErr, Claims: []corev1.PersistentVolumeClaim{*pvc}, Storage: in.Storage}
	users, fields := claimUsers(in.Pods, pvc.Name)
	switch {
	case in.PodsErr != nil:
		d.Findings = append(d.Findings, unavailableSource("pods-unknown", "Pods", "which pods use the claim", in.PodsErr, podGVR, pvc.Namespace))
		d.Findings = append(d.Findings, w.claimChain(nil, pvc, "")...)
	case len(users) == 0:
		d.Summary = append(d.Summary, "used by: no pod")
		d.Findings = append(d.Findings, w.claimChain(nil, pvc, "")...)
	default:
		names := make([]string, 0, len(users))
		for _, u := range users {
			names = append(names, u.Name)
		}
		d.Summary = append(d.Summary, "used by: "+strings.Join(names, ", "))
		for i, u := range users {
			if i == maxClaimUsers {
				d.Findings = append(d.Findings, diagFinding{Code: "more-users", Severity: diagInfo, Observed: in.Now,
					Title: fmt.Sprintf("%d more pods use the claim; only the first %d are explained", len(users)-maxClaimUsers, maxClaimUsers)})
				break
			}
			d.Findings = append(d.Findings, w.claimChain(u, pvc, fields[i])...)
			d.Findings = append(d.Findings, w.volumeEventFindings(u)...)
		}
	}
	if len(d.Findings) == 0 {
		d.Findings = append(d.Findings, diagFinding{
			Code: "no-problem", Severity: diagInfo, Observed: in.Now,
			Title: "kc found no storage problem for this claim",
			Links: []diagLink{{GVR: pvcGVR, Namespace: pvc.Namespace, Name: pvc.Name, Evidence: "status.phase=" + string(pvc.Status.Phase)}},
		})
	}
	sortFindings(d.Findings)
	return d
}

func claimSummary(pvc *corev1.PersistentVolumeClaim) string {
	class := "(none)"
	if pvc.Spec.StorageClassName != nil {
		class = *pvc.Spec.StorageClassName
		if class == "" {
			class = `""`
		}
	}
	s := fmt.Sprintf("%s, class %s", cmpPhase(pvc.Status.Phase), class)
	if pvc.Spec.VolumeName != "" {
		s += ", volume " + pvc.Spec.VolumeName
	}
	return s
}

func cmpPhase(p corev1.PersistentVolumeClaimPhase) string {
	if p == "" {
		return "phase unknown"
	}
	return string(p)
}

// claimDiagnosisRows reads a claim, the pods and events of its namespace
// and the storage objects it names from the cache, and renders the
// explanation.
func (f *RelatedObjectsFolder) claimDiagnosisRows(ctx context.Context) ([]table.Row, error) {
	log := ctrllog.FromContext(ctx).WithName("diagnosis")
	pvc, err := getTyped[corev1.PersistentVolumeClaim](ctx, f.reader, pvcGVR, f.namespace, f.name)
	if err != nil {
		return nil, err
	}
	in := claimInput{Claim: pvc, Now: time.Now()}
	in.Pods, in.PodsErr = listTyped[corev1.Pod](ctx, f.reader, podGVR, f.namespace)
	in.Events, in.EvErr = listTyped[corev1.Event](ctx, f.reader, ovEventsGVR, f.namespace)
	users, _ := claimUsers(in.Pods, pvc.Name)
	if len(users) > maxClaimUsers {
		users = users[:maxClaimUsers]
	}
	if len(users) == 0 {
		// The claim alone: its class or volume, no attachment.
		users = []*corev1.Pod{{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "claim", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name}}}}}}
	}
	in.Storage = readStorage(ctx, f.reader, []corev1.PersistentVolumeClaim{*pvc}, users)
	d := diagnoseClaim(in)
	log.V(1).Info("claim diagnosis", "namespace", f.namespace, "name", f.name, "findings", len(d.Findings))
	return renderDiagnosis(f.Deps, f.rowDeps, f.Path(), d), nil
}
