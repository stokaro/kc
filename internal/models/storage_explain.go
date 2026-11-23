package models

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var (
	storageClassesGVR = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	csiDriversGVR     = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "csidrivers"}
	volumeAttachGVR   = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "volumeattachments"}
)

// Event reasons of the storage path, as the PV controller, external
// provisioners, the attach/detach controller and the kubelet report them.
var (
	claimFailureReasons = []string{"ProvisioningFailed", "FailedBinding", "VolumeMismatch", "ClaimLost"}
	podVolumeReasons    = []string{"FailedAttachVolume", "FailedMount", "FailedMapVolume"}
)

// storageInput is what the storage explanation of one pod or claim reads
// beyond the namespace's claims and events: the cluster-scoped objects the
// claims name. A name in an Errs map could not be read; a NotFound error
// there means the object does not exist.
type storageInput struct {
	Classes     map[string]*storagev1.StorageClass
	ClassErrs   map[string]error
	Volumes     map[string]*corev1.PersistentVolume
	VolumeErrs  map[string]error
	Drivers     map[string]*storagev1.CSIDriver
	DriverErrs  map[string]error
	Attachments []storagev1.VolumeAttachment
	// AttachmentsErr is set when VolumeAttachments could not be listed;
	// AttachmentsRead when they were listed (only for a pod on a node with
	// a CSI volume).
	AttachmentsErr  error
	AttachmentsRead bool
}

// podClaims returns the claims a pod's volumes name, with the field that
// names each.
func podClaims(pod *corev1.Pod) (names, fields []string) {
	for i, v := range pod.Spec.Volumes {
		switch {
		case v.PersistentVolumeClaim != nil:
			names = append(names, v.PersistentVolumeClaim.ClaimName)
			fields = append(fields, fmt.Sprintf("spec.volumes[%d].persistentVolumeClaim", i))
		case v.Ephemeral != nil:
			names = append(names, pod.Name+"-"+v.Name)
			fields = append(fields, fmt.Sprintf("spec.volumes[%d].ephemeral", i))
		}
	}
	return names, fields
}

// storageFindings explains the storage path of each claim the pod uses,
// one stage at a time: the class and its binding mode, provisioning, the
// bound volume, its attachment to the pod's node. A stage kc could not
// read is incomplete, never missing. Without a storage input (a
// workload's diagnosis) it returns nothing.
func (in *workloadInput) storageFindings(pod *corev1.Pod) []diagFinding {
	if in.Storage == nil || in.ClaimsErr != nil {
		return nil
	}
	var out []diagFinding
	names, fields := podClaims(pod)
	for i, name := range names {
		idx := slices.IndexFunc(in.Claims, func(c corev1.PersistentVolumeClaim) bool { return c.Name == name })
		if idx < 0 {
			continue // claimFinding reports the missing claim
		}
		out = append(out, in.claimChain(pod, &in.Claims[idx], fields[i])...)
	}
	return out
}

// claimChain explains one claim for the pod that uses it, or for the claim
// alone when pod is nil (a claim no pod uses).
func (in *workloadInput) claimChain(pod *corev1.Pod, pvc *corev1.PersistentVolumeClaim, field string) []diagFinding {
	code := "storage/" + pvc.Name
	links := []diagLink{{GVR: pvcGVR, Namespace: pvc.Namespace, Name: pvc.Name, Evidence: fmt.Sprintf("status.phase=%s, uid %s", cmp.Or(string(pvc.Status.Phase), "unknown"), pvc.UID)}}
	if pod != nil {
		code = "storage/" + pod.Name + "/" + pvc.Name
		links = append([]diagLink{in.podLink(pod, field+" names "+pvc.Name)}, links...)
	}
	switch pvc.Status.Phase {
	case corev1.ClaimBound:
		return in.boundChain(pod, pvc, code, links)
	case corev1.ClaimLost:
		return []diagFinding{{
			Code: code + "/lost", Severity: diagError, Observed: in.observedFor(pvc.UID),
			Title:  fmt.Sprintf("Claim %s lost its volume %s", pvc.Name, pvc.Spec.VolumeName),
			Detail: "The PersistentVolume the claim was bound to no longer exists or is bound elsewhere; the data may be gone.",
			Links:  append(links, in.eventLinks(pvc.Namespace, pvc.UID, "", false)...),
			Next:   "Restore the volume, or recreate the claim.",
		}}
	default:
		return in.pendingChain(pod, pvc, code, links)
	}
}

// nodeOf is the node a pod is scheduled to; empty for no pod.
func nodeOf(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	return pod.Spec.NodeName
}

// observedFor is the newest event time about uid, or the read time.
func (in *workloadInput) observedFor(uid types.UID) time.Time {
	if t := eventsObserved(in.Events, uid); !t.IsZero() {
		return t
	}
	return in.Now
}

// pendingChain explains an unbound claim: which class provisions it, and
// whether waiting is expected (WaitForFirstConsumer before the pod is
// scheduled) or something reported a failure.
func (in *workloadInput) pendingChain(pod *corev1.Pod, pvc *corev1.PersistentVolumeClaim, code string, links []diagLink) []diagFinding {
	if failed, ok := in.newestEvent(pvc.UID, claimFailureReasons); ok {
		return []diagFinding{{
			Code: code + "/" + strings.ToLower(failed.Reason), Severity: diagError, Observed: eventTime(failed),
			Title:  fmt.Sprintf("Claim %s: %s: %s", pvc.Name, failed.Reason, oneLine(failed.Message)),
			Detail: "Reported by " + cmp.Or(failed.Source.Component, failed.ReportingController, "the cluster") + ".",
			Links:  append(links, in.eventLinks(pvc.Namespace, pvc.UID, "", true)...),
			Next:   "Fix what the message names (class parameters, quota, capacity, access modes), then the provisioner retries.",
		}}
	}
	class := pvc.Spec.StorageClassName
	switch {
	case class == nil:
		return []diagFinding{{
			Code: code + "/no-class", Severity: diagWarning, Observed: in.Now, Links: links,
			Title:  fmt.Sprintf("Claim %s names no StorageClass and is not bound", pvc.Name),
			Detail: "No default StorageClass existed when the claim was created. It binds to a matching PersistentVolume, or gets the default class once one exists.",
			Next:   "Set spec.storageClassName in a new claim, or mark a StorageClass as default.",
		}}
	case *class == "":
		return []diagFinding{{
			Code: code + "/static", Severity: diagWarning, Observed: in.Now, Links: links,
			Title:  fmt.Sprintf("Claim %s asks for a pre-created volume (storageClassName \"\") and none matches", pvc.Name),
			Detail: "Nothing provisions it: it binds only to an Available PersistentVolume without a class whose size, access modes and selector match.",
			Next:   "Create a matching PersistentVolume, or use a StorageClass.",
		}}
	}
	sc, err := in.Storage.class(*class)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []diagFinding{{
				Code: code + "/class-missing", Severity: diagError, Observed: in.Now,
				Title: fmt.Sprintf("Claim %s uses StorageClass %s, which does not exist", pvc.Name, *class),
				Links: append(links, diagLink{GVR: storageClassesGVR, Name: *class, Evidence: "spec.storageClassName", Problem: problemNotFound}),
				Next:  "Create the StorageClass, or recreate the claim with an existing one.",
			}}
		}
		return []diagFinding{unavailableSource(code+"/class-unknown", "StorageClass "+*class, "its provisioner and binding mode", err, storageClassesGVR, "")}
	}
	classLink := diagLink{GVR: storageClassesGVR, Name: sc.Name, Evidence: fmt.Sprintf("provisioner %s, volumeBindingMode %s", sc.Provisioner, bindingMode(sc))}
	links = append(links, classLink)
	selected := pvc.Annotations["volume.kubernetes.io/selected-node"]
	if bindingMode(sc) == storagev1.VolumeBindingWaitForFirstConsumer && nodeOf(pod) == "" && selected == "" {
		f := diagFinding{
			Code: code + "/wait-for-consumer", Severity: diagInfo, Observed: in.Now, Links: links,
			Title:  fmt.Sprintf("Claim %s waits for the pod to be scheduled (WaitForFirstConsumer)", pvc.Name),
			Detail: "This is expected: the volume is provisioned on the node the scheduler picks, so the claim stays Pending until then.",
			Next:   "If the pod stays Pending, the scheduling findings explain why; the claim is not the cause.",
		}
		if pod == nil {
			f.Title = fmt.Sprintf("Claim %s waits for a pod that uses it (WaitForFirstConsumer)", pvc.Name)
			f.Next = "No pod uses the claim yet; it binds once one is scheduled."
		}
		return []diagFinding{f}
	}
	f := diagFinding{
		Code: code + "/provisioning", Severity: diagWarning, Observed: in.observedFor(pvc.UID),
		Title: fmt.Sprintf("Claim %s waits for provisioner %s", pvc.Name, sc.Provisioner),
		Links: append(links, in.eventLinks(pvc.Namespace, pvc.UID, "", false)...),
		Next:  "Check that the provisioner runs (its controller pods and logs); kc does not inspect it.",
	}
	if selected != "" {
		f.Detail = "The scheduler selected node " + selected + " (annotation volume.kubernetes.io/selected-node); the provisioner has not created a volume yet."
	}
	return []diagFinding{f}
}

// boundChain explains a bound claim: the volume exists, and is attached to
// the pod's node when its driver needs an attachment. Bound does not mean
// mounted; the kubelet's mount events are reported separately.
func (in *workloadInput) boundChain(pod *corev1.Pod, pvc *corev1.PersistentVolumeClaim, code string, links []diagLink) []diagFinding {
	name := pvc.Spec.VolumeName
	pv, err := in.Storage.volume(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []diagFinding{{
				Code: code + "/pv-missing", Severity: diagError, Observed: in.Now,
				Title: fmt.Sprintf("Claim %s is bound to volume %s, which does not exist", pvc.Name, name),
				Links: append(links, diagLink{GVR: pvGVR, Name: name, Evidence: "spec.volumeName", Problem: problemNotFound}),
				Next:  "The claim will turn Lost; restore the volume or recreate the claim.",
			}}
		}
		return []diagFinding{unavailableSource(code+"/pv-unknown", "PersistentVolume "+name, "its phase, driver and attachment", err, pvGVR, "")}
	}
	pvLink := diagLink{GVR: pvGVR, Name: pv.Name, Evidence: "status.phase=" + string(pv.Status.Phase) + volumeSourceText(pv)}
	links = append(links, pvLink)
	if pv.Status.Phase == corev1.VolumeFailed {
		return []diagFinding{{
			Code: code + "/pv-failed", Severity: diagError, Observed: in.Now, Links: links,
			Title:  fmt.Sprintf("Volume %s of claim %s is Failed: %s", pv.Name, pvc.Name, cmp.Or(pv.Status.Message, pv.Status.Reason, "no message")),
			Detail: "The PV controller could not reclaim or recycle the volume.",
		}}
	}
	if pv.Spec.CSI == nil || nodeOf(pod) == "" || !in.Storage.AttachmentsRead && in.Storage.AttachmentsErr == nil {
		return nil
	}
	return in.attachFinding(pod, pvc, pv, code, links)
}

// attachFinding checks the VolumeAttachment of a CSI volume on the pod's
// node. An absent attachment is a problem only when the driver needs one;
// an unknown driver makes it an open question, not a failure.
func (in *workloadInput) attachFinding(pod *corev1.Pod, pvc *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, code string, links []diagLink) []diagFinding {
	if in.Storage.AttachmentsErr != nil {
		return []diagFinding{unavailableSource(code+"/attach-unknown", "VolumeAttachments", "whether volume "+pv.Name+" is attached to node "+pod.Spec.NodeName, in.Storage.AttachmentsErr, volumeAttachGVR, "")}
	}
	node := pod.Spec.NodeName
	idx := slices.IndexFunc(in.Storage.Attachments, func(va storagev1.VolumeAttachment) bool {
		return va.Spec.NodeName == node && va.Spec.Source.PersistentVolumeName != nil && *va.Spec.Source.PersistentVolumeName == pv.Name
	})
	if idx >= 0 {
		va := &in.Storage.Attachments[idx]
		vaLink := diagLink{GVR: volumeAttachGVR, Name: va.Name, Evidence: fmt.Sprintf("attacher %s, node %s, status.attached=%v", va.Spec.Attacher, node, va.Status.Attached)}
		switch {
		case va.Status.AttachError != nil:
			return []diagFinding{{
				Code: code + "/attach-error", Severity: diagError, Observed: va.Status.AttachError.Time.Time,
				Title: fmt.Sprintf("Volume %s cannot be attached to node %s: %s", pv.Name, node, oneLine(va.Status.AttachError.Message)),
				Links: append(links, vaLink),
				Next:  "The message is the CSI driver's; read its controller plugin logs. kc does not inspect the driver.",
			}}
		case !va.Status.Attached:
			return []diagFinding{{
				Code: code + "/attaching", Severity: diagInfo, Observed: in.Now, Links: append(links, vaLink),
				Title: fmt.Sprintf("Volume %s is being attached to node %s", pv.Name, node),
			}}
		}
		return nil
	}
	driver := pv.Spec.CSI.Driver
	d, err := in.Storage.driver(driver)
	if err == nil && d.Spec.AttachRequired != nil && !*d.Spec.AttachRequired {
		return nil // the driver needs no attachment
	}
	f := diagFinding{
		Code: code + "/no-attachment", Severity: diagInfo, Observed: in.Now, Links: links,
		Title: fmt.Sprintf("No VolumeAttachment for volume %s on node %s yet", pv.Name, node),
		Next:  "The attach/detach controller creates it after the pod is scheduled; the pod's FailedAttachVolume events say why it does not.",
	}
	switch {
	case err == nil:
		f.Detail = "CSIDriver " + driver + " requires attachment."
		f.Links = append(f.Links, diagLink{GVR: csiDriversGVR, Name: driver, Evidence: "spec.attachRequired is true or unset"})
	case apierrors.IsNotFound(err):
		f.Detail = "No CSIDriver object named " + driver + " exists, so Kubernetes treats it as requiring attachment."
	default:
		f.Severity = diagIncomplete
		f.Detail = "kc could not read CSIDriver " + driver + " (" + readProblem(err) + "), so it does not know whether the driver needs an attachment."
	}
	return []diagFinding{f}
}

// volumeEventFindings reports the kubelet's and the attach/detach
// controller's volume failures for a pod that has not started on its
// node. They are what was reported; kc does not inspect the node, the
// filesystem or the driver.
func (in *workloadInput) volumeEventFindings(pod *corev1.Pod) []diagFinding {
	if pod.Spec.NodeName == "" || pod.Status.Phase != corev1.PodPending || in.EventsErr != nil {
		return nil
	}
	var out []diagFinding
	for _, reason := range podVolumeReasons {
		ev, ok := in.newestEvent(pod.UID, []string{reason})
		if !ok {
			continue
		}
		f := diagFinding{
			Code: "volume/" + pod.Name + "/" + strings.ToLower(reason), Severity: diagError, Observed: eventTime(ev),
			Title:  fmt.Sprintf("Pod %s: %s: %s", pod.Name, reason, oneLine(ev.Message)),
			Detail: "Reported by " + cmp.Or(ev.Source.Component, ev.ReportingController, "the cluster") + volumeEventCount(ev) + ". kc does not inspect the node, the filesystem or the CSI driver.",
			Links:  append([]diagLink{in.podLink(pod, "spec.nodeName="+pod.Spec.NodeName+", status.phase=Pending")}, in.eventLinks(pod.Namespace, pod.UID, "", true)...),
			Next:   volumeEventNext(reason),
		}
		if in.Storage == nil {
			f.Hint = "The pod's own /diagnosis follows its claims to the volume and its attachment."
		}
		out = append(out, f)
	}
	return out
}

func volumeEventNext(reason string) string {
	switch reason {
	case "FailedAttachVolume":
		return "Check the volume's VolumeAttachment and the CSI controller plugin; a volume attached to another node must be detached first."
	case "FailedMapVolume":
		return "Check the block device on the node and the CSI node plugin."
	default:
		return "Check the Secret, ConfigMap or claim the message names, and the CSI node plugin logs on the node."
	}
}

func volumeEventCount(ev *corev1.Event) string {
	if ev.Count > 1 {
		return fmt.Sprintf(", %d times", ev.Count)
	}
	return ""
}

// newestEvent returns the newest event about uid with one of reasons.
func (in *workloadInput) newestEvent(uid types.UID, reasons []string) (*corev1.Event, bool) {
	var best *corev1.Event
	for i := range in.Events {
		ev := &in.Events[i]
		if ev.InvolvedObject.UID != uid || !slices.Contains(reasons, ev.Reason) {
			continue
		}
		if best == nil || eventTime(ev).After(eventTime(best)) {
			best = ev
		}
	}
	return best, best != nil
}

func bindingMode(sc *storagev1.StorageClass) storagev1.VolumeBindingMode {
	if sc.VolumeBindingMode == nil {
		return storagev1.VolumeBindingImmediate
	}
	return *sc.VolumeBindingMode
}

func volumeSourceText(pv *corev1.PersistentVolume) string {
	switch {
	case pv.Spec.CSI != nil:
		return ", CSI driver " + pv.Spec.CSI.Driver
	case pv.Spec.Local != nil:
		return ", local " + pv.Spec.Local.Path
	case pv.Spec.HostPath != nil:
		return ", hostPath " + pv.Spec.HostPath.Path
	case pv.Spec.NFS != nil:
		return ", NFS " + pv.Spec.NFS.Server
	}
	return ""
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (s *storageInput) class(name string) (*storagev1.StorageClass, error) {
	if err := s.ClassErrs[name]; err != nil {
		return nil, err
	}
	if c := s.Classes[name]; c != nil {
		return c, nil
	}
	return nil, apierrors.NewNotFound(storageClassesGVR.GroupResource(), name)
}

func (s *storageInput) volume(name string) (*corev1.PersistentVolume, error) {
	if err := s.VolumeErrs[name]; err != nil {
		return nil, err
	}
	if v := s.Volumes[name]; v != nil {
		return v, nil
	}
	return nil, apierrors.NewNotFound(pvGVR.GroupResource(), name)
}

func (s *storageInput) driver(name string) (*storagev1.CSIDriver, error) {
	if err := s.DriverErrs[name]; err != nil {
		return nil, err
	}
	if d := s.Drivers[name]; d != nil {
		return d, nil
	}
	return nil, apierrors.NewNotFound(csiDriversGVR.GroupResource(), name)
}
