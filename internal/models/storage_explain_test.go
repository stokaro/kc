package models

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

var storageNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func storagePod(node string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		Namespace: "shop", Name: "db-0", UID: "pod-uid",
		Spec: corev1.PodSpec{NodeName: node, Volumes: []corev1.Volume{{Name: "data",
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func claim(phase corev1.PersistentVolumeClaimPhase, class *string, volume string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		Namespace: "shop", Name: "data", UID: "pvc-uid",
		Spec:   corev1.PersistentVolumeClaimSpec{StorageClassName: class, VolumeName: volume},
		Status: corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

func class(name string, mode storagev1.VolumeBindingMode) *storagev1.StorageClass {
	return &storagev1.StorageClass{Name: name, Provisioner: "ebs.csi.aws.com", VolumeBindingMode: &mode}
}

func csiVolume(name string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{Name: name,
		Spec:   corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "ebs.csi.aws.com", VolumeHandle: "vol-1"}}},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
}

func attachment(pv, node string, attached bool, attachErr string) storagev1.VolumeAttachment {
	va := storagev1.VolumeAttachment{Name: "csi-abc",
		Spec:   storagev1.VolumeAttachmentSpec{Attacher: "ebs.csi.aws.com", NodeName: node, Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: &pv}},
		Status: storagev1.VolumeAttachmentStatus{Attached: attached}}
	if attachErr != "" {
		va.Status.AttachError = &storagev1.VolumeError{Message: attachErr, Time: metav1.NewTime(storageNow.Add(-time.Minute))}
	}
	return va
}

func newStorage() *storageInput {
	return &storageInput{
		Classes: make(map[string]*storagev1.StorageClass), ClassErrs: make(map[string]error),
		Volumes: make(map[string]*corev1.PersistentVolume), VolumeErrs: make(map[string]error),
		Drivers: make(map[string]*storagev1.CSIDriver), DriverErrs: make(map[string]error),
	}
}

func storageEvent(uid types.UID, reason, typ, msg string) corev1.Event {
	return corev1.Event{
		Namespace: "shop", Name: reason + "-ev",
		InvolvedObject: corev1.ObjectReference{UID: uid}, Reason: reason, Type: typ, Message: msg,
		LastTimestamp: metav1.NewTime(storageNow.Add(-2 * time.Minute)), Count: 3,
	}
}

func forbidden(resource string) error {
	return apierrors.NewForbidden(pvGVR.GroupResource(), "", errors.New(resource))
}

func storageCodes(fs []diagFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code+"="+f.Severity.String())
	}
	return out
}

func TestStorageFindings(t *testing.T) {
	t.Parallel()
	wfc, immediate := storagev1.VolumeBindingWaitForFirstConsumer, storagev1.VolumeBindingImmediate
	gp3 := "gp3"
	empty := ""
	noAttach := false
	tests := []struct {
		name   string
		pod    *corev1.Pod
		claim  corev1.PersistentVolumeClaim
		events []corev1.Event
		setup  func(*storageInput)
		want   []string
		check  func(*testing.T, []diagFinding)
	}{
		{
			name: "bound and attached: nothing to explain", pod: storagePod("n1", corev1.PodRunning),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.Drivers["ebs.csi.aws.com"] = &storagev1.CSIDriver{Name: "ebs.csi.aws.com"}
				s.AttachmentsRead, s.Attachments = true, []storagev1.VolumeAttachment{attachment("pv-1", "n1", true, "")}
			},
			want: nil,
		},
		{
			name: "wait for first consumer before scheduling is expected", pod: storagePod("", corev1.PodPending),
			claim: claim(corev1.ClaimPending, &gp3, ""),
			setup: func(s *storageInput) { s.Classes["gp3"] = class("gp3", wfc) },
			want:  []string{"storage/db-0/data/wait-for-consumer=info"},
		},
		{
			name: "provisioning failure from the claim's events", pod: storagePod("n1", corev1.PodPending),
			claim:  claim(corev1.ClaimPending, &gp3, ""),
			events: []corev1.Event{storageEvent("pvc-uid", "ProvisioningFailed", corev1.EventTypeWarning, "failed to provision volume: quota exceeded")},
			setup:  func(s *storageInput) { s.Classes["gp3"] = class("gp3", immediate) },
			want:   []string{"storage/db-0/data/provisioningfailed=error"},
			check: func(t *testing.T, fs []diagFinding) {
				if !strings.Contains(fs[0].Title, "quota exceeded") || !fs[0].Observed.Equal(storageNow.Add(-2*time.Minute)) {
					t.Errorf("finding %q observed %v, want the event's message and time", fs[0].Title, fs[0].Observed)
				}
				if !slices.ContainsFunc(fs[0].Links, func(l diagLink) bool { return l.GVR == ovEventsGVR }) {
					t.Errorf("no event link: %+v", fs[0].Links)
				}
			},
		},
		{
			name: "immediate class, scheduled, no event: waiting for the provisioner", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimPending, &gp3, ""),
			setup: func(s *storageInput) { s.Classes["gp3"] = class("gp3", immediate) },
			want:  []string{"storage/db-0/data/provisioning=warning"},
		},
		{
			name: "missing class", pod: storagePod("", corev1.PodPending),
			claim: claim(corev1.ClaimPending, &gp3, ""),
			setup: func(s *storageInput) {
				s.ClassErrs["gp3"] = apierrors.NewNotFound(storageClassesGVR.GroupResource(), "gp3")
			},
			want: []string{"storage/db-0/data/class-missing=error"},
		},
		{
			name: "forbidden class is incomplete, not missing", pod: storagePod("", corev1.PodPending),
			claim: claim(corev1.ClaimPending, &gp3, ""),
			setup: func(s *storageInput) { s.ClassErrs["gp3"] = forbidden("storageclasses") },
			want:  []string{"storage/db-0/data/class-unknown=incomplete"},
		},
		{
			name: "static binding with no match", pod: storagePod("", corev1.PodPending),
			claim: claim(corev1.ClaimPending, &empty, ""),
			want:  []string{"storage/db-0/data/static=warning"},
		},
		{
			name: "forbidden volume is incomplete, not missing", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) { s.VolumeErrs["pv-1"] = forbidden("persistentvolumes") },
			want:  []string{"storage/db-0/data/pv-unknown=incomplete"},
		},
		{
			name: "missing volume", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) { s.VolumeErrs["pv-1"] = apierrors.NewNotFound(pvGVR.GroupResource(), "pv-1") },
			want:  []string{"storage/db-0/data/pv-missing=error"},
		},
		{
			name: "attach error", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.AttachmentsRead, s.Attachments = true, []storagev1.VolumeAttachment{
					attachment("pv-1", "n2", true, ""), // another node: not this pod's
					attachment("pv-1", "n1", false, "rpc error: volume is attached to another instance"),
				}
			},
			want: []string{"storage/db-0/data/attach-error=error"},
			check: func(t *testing.T, fs []diagFinding) {
				if !fs[0].Observed.Equal(storageNow.Add(-time.Minute)) || !strings.Contains(fs[0].Title, "another instance") {
					t.Errorf("attach error %q observed %v", fs[0].Title, fs[0].Observed)
				}
			},
		},
		{
			name: "no attachment for a driver that needs none", pod: storagePod("n1", corev1.PodRunning),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.Drivers["ebs.csi.aws.com"] = &storagev1.CSIDriver{Name: "ebs.csi.aws.com", Spec: storagev1.CSIDriverSpec{AttachRequired: &noAttach}}
				s.AttachmentsRead = true
			},
			want: nil,
		},
		{
			name: "no attachment, no CSIDriver object", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.DriverErrs["ebs.csi.aws.com"] = apierrors.NewNotFound(csiDriversGVR.GroupResource(), "ebs.csi.aws.com")
				s.AttachmentsRead = true
			},
			want: []string{"storage/db-0/data/no-attachment=info"},
		},
		{
			name: "no attachment, forbidden CSIDriver", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.DriverErrs["ebs.csi.aws.com"] = forbidden("csidrivers")
				s.AttachmentsRead = true
			},
			want: []string{"storage/db-0/data/no-attachment=incomplete"},
		},
		{
			name: "forbidden attachments", pod: storagePod("n1", corev1.PodPending),
			claim: claim(corev1.ClaimBound, &gp3, "pv-1"),
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.AttachmentsErr = forbidden("volumeattachments")
			},
			want: []string{"storage/db-0/data/attach-unknown=incomplete"},
		},
		{
			name: "mount failure reported by the kubelet", pod: storagePod("n1", corev1.PodPending),
			claim:  claim(corev1.ClaimBound, &gp3, "pv-1"),
			events: []corev1.Event{storageEvent("pod-uid", "FailedMount", corev1.EventTypeWarning, "MountVolume.SetUp failed for volume \"pv-1\": mount failed: exit status 32")},
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.AttachmentsRead, s.Attachments = true, []storagev1.VolumeAttachment{attachment("pv-1", "n1", true, "")}
			},
			want: []string{"volume/db-0/failedmount=error"},
			check: func(t *testing.T, fs []diagFinding) {
				if !strings.Contains(fs[0].Detail, "3 times") || !strings.Contains(fs[0].Detail, "does not inspect the node") {
					t.Errorf("detail = %q", fs[0].Detail)
				}
			},
		},
		{
			name: "an old mount failure of a running pod is not reported", pod: storagePod("n1", corev1.PodRunning),
			claim:  claim(corev1.ClaimBound, &gp3, "pv-1"),
			events: []corev1.Event{storageEvent("pod-uid", "FailedMount", corev1.EventTypeWarning, "timed out")},
			setup: func(s *storageInput) {
				s.Volumes["pv-1"] = csiVolume("pv-1")
				s.AttachmentsRead, s.Attachments = true, []storagev1.VolumeAttachment{attachment("pv-1", "n1", true, "")}
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newStorage()
			if tt.setup != nil {
				tt.setup(s)
			}
			in := workloadInput{GVR: podGVR, Now: storageNow, Events: tt.events, Claims: []corev1.PersistentVolumeClaim{tt.claim}, Storage: s}
			fs := append(in.storageFindings(tt.pod), in.volumeEventFindings(tt.pod)...)
			if got := storageCodes(fs); !slices.Equal(got, tt.want) {
				t.Fatalf("findings = %v, want %v", got, tt.want)
			}
			if _, ok := in.claimFinding(tt.pod, "data", "spec.volumes[0]"); ok {
				t.Errorf("the generic claim finding is reported next to the storage explanation")
			}
			if tt.check != nil {
				tt.check(t, fs)
			}
		})
	}
}

// A workload's diagnosis reads no storage objects: an unbound claim keeps
// its generic warning, and the kubelet's mount events still show.
func TestWorkloadVolumeEventsWithoutStorage(t *testing.T) {
	t.Parallel()
	pod := storagePod("n1", corev1.PodPending)
	in := workloadInput{GVR: relDeploymentsGVR, Now: storageNow,
		Claims: []corev1.PersistentVolumeClaim{claim(corev1.ClaimPending, nil, "")},
		Events: []corev1.Event{storageEvent("pod-uid", "FailedAttachVolume", corev1.EventTypeWarning, "Multi-Attach error for volume pv-1")}}
	if fs := in.storageFindings(pod); len(fs) != 0 {
		t.Fatalf("storage findings without a storage input: %v", storageCodes(fs))
	}
	if _, ok := in.claimFinding(pod, "data", "spec.volumes[0]"); !ok {
		t.Error("no generic claim warning")
	}
	fs := in.volumeEventFindings(pod)
	if got := storageCodes(fs); !slices.Equal(got, []string{"volume/db-0/failedattachvolume=error"}) || fs[0].Hint == "" {
		t.Fatalf("findings = %v hint %q, want the attach failure and a pointer to the pod's diagnosis", got, fs[0].Hint)
	}
}

func TestDiagnoseClaim(t *testing.T) {
	t.Parallel()
	gp3 := "gp3"
	wfc := storagev1.VolumeBindingWaitForFirstConsumer
	s := newStorage()
	s.Classes["gp3"] = class("gp3", wfc)
	pvc := claim(corev1.ClaimPending, &gp3, "")

	t.Run("no user", func(t *testing.T) {
		t.Parallel()
		d := diagnoseClaim(claimInput{Claim: &pvc, Now: storageNow, Storage: s})
		if got := storageCodes(d.Findings); !slices.Equal(got, []string{"storage/data/wait-for-consumer=info"}) {
			t.Fatalf("findings = %v", got)
		}
		if !strings.Contains(d.Findings[0].Title, "waits for a pod that uses it") || !slices.Contains(d.Summary, "used by: no pod") {
			t.Errorf("title %q summary %v", d.Findings[0].Title, d.Summary)
		}
	})
	t.Run("a user and an unrelated pod", func(t *testing.T) {
		t.Parallel()
		other := corev1.Pod{Namespace: "shop", Name: "web-0"}
		d := diagnoseClaim(claimInput{Claim: &pvc, Now: storageNow, Storage: s, Pods: []corev1.Pod{other, *storagePod("", corev1.PodPending)}})
		if got := storageCodes(d.Findings); !slices.Equal(got, []string{"storage/db-0/data/wait-for-consumer=info"}) {
			t.Fatalf("findings = %v", got)
		}
		if !slices.Contains(d.Summary, "used by: db-0") {
			t.Errorf("summary %v", d.Summary)
		}
	})
	t.Run("pods unreadable", func(t *testing.T) {
		t.Parallel()
		d := diagnoseClaim(claimInput{Claim: &pvc, Now: storageNow, Storage: s, PodsErr: forbidden("pods")})
		if got := storageCodes(d.Findings); !slices.Contains(got, "pods-unknown=incomplete") {
			t.Fatalf("findings = %v", got)
		}
	})
}

// The pod's own diagnosis replaces the generic "not Bound" warning with
// the storage explanation.
func TestPodDiagnosisExplainsStorage(t *testing.T) {
	t.Parallel()
	gp3 := "gp3"
	s := newStorage()
	s.Classes["gp3"] = class("gp3", storagev1.VolumeBindingWaitForFirstConsumer)
	pod := storagePod("", corev1.PodPending)
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	d, err := diagnoseWorkload(workloadInput{GVR: podGVR, Object: &unstructured.Unstructured{Object: raw}, Now: storageNow,
		Claims: []corev1.PersistentVolumeClaim{claim(corev1.ClaimPending, &gp3, "")}, Storage: s})
	if err != nil {
		t.Fatal(err)
	}
	got := storageCodes(d.Findings)
	if !slices.Contains(got, "storage/db-0/data/wait-for-consumer=info") || slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, "claim/") }) {
		t.Fatalf("findings = %v, want the WaitForFirstConsumer explanation and no generic claim warning", got)
	}
}
