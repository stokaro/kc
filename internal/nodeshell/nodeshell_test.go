package nodeshell_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stokaro/kc/internal/nodeshell"
	"github.com/stokaro/kc/pkg/appconfig"
)

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func TestNewPod(t *testing.T) {
	t.Parallel()
	opts := nodeshell.Options{Image: "example.com/tools:1", Namespace: "ops", MaxDuration: 90 * time.Minute}
	pod := nodeshell.NewPod("worker-1", "kc-node-shell-worker-1-abcde", opts)

	if pod.Name != "kc-node-shell-worker-1-abcde" || pod.Namespace != "ops" {
		t.Fatalf("pod = %s/%s, want ops/kc-node-shell-worker-1-abcde", pod.Namespace, pod.Name)
	}
	spec := pod.Spec
	if spec.NodeName != "worker-1" {
		t.Errorf("nodeName = %q, want worker-1", spec.NodeName)
	}
	if !spec.HostPID || !spec.HostNetwork || !spec.HostIPC {
		t.Errorf("host namespaces = pid %v net %v ipc %v, want all true", spec.HostPID, spec.HostNetwork, spec.HostIPC)
	}
	if spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", spec.RestartPolicy)
	}
	if want := []corev1.Toleration{{Operator: corev1.TolerationOpExists}}; !slices.Equal(spec.Tolerations, want) {
		t.Errorf("tolerations = %+v, want %+v", spec.Tolerations, want)
	}
	if spec.ActiveDeadlineSeconds == nil || *spec.ActiveDeadlineSeconds != 5400 {
		t.Errorf("activeDeadlineSeconds = %v, want 5400", spec.ActiveDeadlineSeconds)
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Errorf("automountServiceAccountToken = %v, want false", spec.AutomountServiceAccountToken)
	}
	if len(spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(spec.Containers))
	}
	c := spec.Containers[0]
	if c.Name != nodeshell.ContainerName || c.Image != "example.com/tools:1" {
		t.Errorf("container = %s %s, want %s example.com/tools:1", c.Name, c.Image, nodeshell.ContainerName)
	}
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Errorf("container is not privileged: %+v", c.SecurityContext)
	}
	// The container must exit on its own when no shell keeps it alive.
	if script := strings.Join(c.Command, " "); !strings.Contains(script, "-lt 60") {
		t.Errorf("command %q has no 60 second idle exit", script)
	}
	if pod.Labels[nodeshell.LabelComponent] != nodeshell.ComponentValue {
		t.Errorf("labels = %v, want %s=%s", pod.Labels, nodeshell.LabelComponent, nodeshell.ComponentValue)
	}
	if pod.Annotations[nodeshell.AnnotationNode] != "worker-1" {
		t.Errorf("annotations = %v, want the node", pod.Annotations)
	}
}

func TestOptionsFromDefaults(t *testing.T) {
	t.Parallel()
	pod := nodeshell.NewPod("n", "p", nodeshell.OptionsFrom(appconfig.NodeShellConfig{}))
	if pod.Namespace != appconfig.DefaultNodeShellNamespace {
		t.Errorf("namespace = %q, want %q", pod.Namespace, appconfig.DefaultNodeShellNamespace)
	}
	if got := pod.Spec.Containers[0].Image; got != appconfig.DefaultNodeShellImage {
		t.Errorf("image = %q, want %q", got, appconfig.DefaultNodeShellImage)
	}
	if got, want := *pod.Spec.ActiveDeadlineSeconds, int64(appconfig.DefaultNodeShellMaxDuration/time.Second); got != want {
		t.Errorf("activeDeadlineSeconds = %d, want %d", got, want)
	}
}

func TestShellCommandEntersHostNamespaces(t *testing.T) {
	t.Parallel()
	got := strings.Join(nodeshell.ShellCommand(), " ")
	if want := "nsenter -t 1 -m -u -i -n -p -- sh -c "; !strings.HasPrefix(got, want) {
		t.Errorf("shell command %q does not start with %q", got, want)
	}
	// The keepalive must stop touching the heartbeat when kc stops
	// writing, so it reads with a timeout.
	keep := strings.Join(nodeshell.KeepaliveCommand(), " ")
	for _, want := range []string{"while read -t 30 _", "touch /tmp/.kc-node-shell-alive"} {
		if !strings.Contains(keep, want) {
			t.Errorf("keepalive %q lacks %q", keep, want)
		}
	}
	idle := strings.Join(nodeshell.NewPod("n", "p", nodeshell.Options{}).Spec.Containers[0].Command, " ")
	if !strings.Contains(idle, "stat -c %Y /tmp/.kc-node-shell-alive") {
		t.Errorf("idle script %q does not watch the keepalive's heartbeat file", idle)
	}
}

func TestPodName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, node, wantBase string
	}{
		{name: "plain", node: "worker-1", wantBase: "kc-node-shell-worker-1-"},
		{name: "dots and case", node: "Ip-10-0-1-2.EC2.internal", wantBase: "kc-node-shell-ip-10-0-1-2-ec2-internal-"},
		{name: "long", node: strings.Repeat("a", 100), wantBase: "kc-node-shell-" + strings.Repeat("a", 43) + "-"},
		{name: "only symbols", node: "...", wantBase: "kc-node-shell-node-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := nodeshell.PodName(tt.node)
			if !strings.HasPrefix(got, tt.wantBase) || len(got) != len(tt.wantBase)+5 {
				t.Errorf("PodName(%q) = %q, want %q plus 5 characters", tt.node, got, tt.wantBase)
			}
			if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
				t.Errorf("PodName(%q) = %q is no DNS label: %v", tt.node, got, errs)
			}
		})
	}
	if nodeshell.PodName("n") == nodeshell.PodName("n") {
		t.Errorf("two names for the same node are equal")
	}
}

func TestKubectlDebugArgs(t *testing.T) {
	t.Parallel()
	got := strings.Join(nodeshell.KubectlDebugArgs("worker-1", nodeshell.Options{Image: "busybox:1", Namespace: "ops"}), " ")
	want := "debug node/worker-1 -n ops -it --profile=sysadmin --image=busybox:1 -- nsenter -t 1 -m -u -i -n -p -- sh"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

// statusReactor answers pod reads with the stored pod and the status that
// status returns for the n-th read.
func statusReactor(t *testing.T, client *fake.Clientset, status func(n int) corev1.PodStatus) {
	t.Helper()
	reads := 0
	client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		get := action.(k8stesting.GetAction)
		obj, err := client.Tracker().Get(podsGVR, get.GetNamespace(), get.GetName())
		if err != nil {
			return true, nil, err
		}
		pod := obj.(*corev1.Pod).DeepCopy()
		reads++
		pod.Status = status(reads)
		return true, pod, nil
	})
}

func shellStatus(state corev1.ContainerState) corev1.PodStatus {
	return corev1.PodStatus{
		Phase:             corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{Name: nodeshell.ContainerName, State: state}},
	}
}

func fastOptions() nodeshell.Options {
	return nodeshell.Options{Namespace: "ops", ReadyTimeout: 300 * time.Millisecond, PollInterval: 5 * time.Millisecond}
}

func podExists(t *testing.T, client *fake.Clientset, name string) bool {
	t.Helper()
	_, err := client.Tracker().Get(podsGVR, "ops", name)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("tracker get: %v", err)
	}
	return err == nil
}

func TestStartWaitsUntilTheShellContainerRuns(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset()
	statusReactor(t, client, func(n int) corev1.PodStatus {
		if n < 3 {
			return shellStatus(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}})
		}
		st := shellStatus(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})
		st.Phase = corev1.PodRunning
		return st
	})

	pod, err := nodeshell.Start(t.Context(), client, "worker-1", "shell-a", fastOptions())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pod.Status.Phase != corev1.PodRunning {
		t.Errorf("phase = %s, want Running", pod.Status.Phase)
	}
	if !podExists(t, client, "shell-a") {
		t.Errorf("running pod was deleted")
	}
	created := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" && a.GetResource() == podsGVR {
			created++
		}
	}
	if created != 1 {
		t.Errorf("creates = %d, want 1", created)
	}
}

func TestStartDeletesAPodThatCannotStart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  corev1.PodStatus
		wantErr string
	}{
		{
			name:    "image pull",
			status:  shellStatus(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "not found"}}),
			wantErr: "ErrImagePull",
		},
		{
			name:    "failed",
			status:  corev1.PodStatus{Phase: corev1.PodFailed, Message: "DeadlineExceeded"},
			wantErr: "Failed",
		},
		{
			name:    "never runs",
			status:  shellStatus(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}),
			wantErr: "did not start",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewClientset()
			statusReactor(t, client, func(int) corev1.PodStatus { return tt.status })

			_, err := nodeshell.Start(t.Context(), client, "worker-1", "shell-b", fastOptions())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Start error = %v, want one with %q", err, tt.wantErr)
			}
			if podExists(t, client, "shell-b") {
				t.Errorf("pod that did not start was left behind")
			}
		})
	}
}

func TestStartCleansUpWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset()
	ctx, cancel := context.WithCancel(t.Context())
	statusReactor(t, client, func(n int) corev1.PodStatus {
		if n == 2 {
			cancel() // kc quits while the pod starts
		}
		return shellStatus(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}})
	})
	opts := fastOptions()
	opts.ReadyTimeout = time.Minute

	if _, err := nodeshell.Start(ctx, client, "worker-1", "shell-c", opts); err == nil {
		t.Fatalf("Start succeeded after the context ended")
	}
	if podExists(t, client, "shell-c") {
		t.Errorf("pod left behind after the context ended")
	}
}

func TestStartReportsCreateErrors(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset()
	client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(podsGVR.GroupResource(), "shell-d", errPrivileged)
	})
	_, err := nodeshell.Start(t.Context(), client, "worker-1", "shell-d", fastOptions())
	if !apierrors.IsForbidden(err) {
		t.Fatalf("Start error = %v, want Forbidden", err)
	}
}

var errPrivileged = &privilegedErr{}

type privilegedErr struct{}

func (*privilegedErr) Error() string { return `violates PodSecurity "baseline:latest": privileged` }

func TestDelete(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{Name: "shell-e", Namespace: "ops", UID: types.UID("uid-1")}
	client := fake.NewClientset(pod)

	if err := nodeshell.Delete(t.Context(), client, pod); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if podExists(t, client, "shell-e") {
		t.Errorf("pod still exists")
	}
	var del k8stesting.DeleteAction
	for _, a := range client.Actions() {
		if d, ok := a.(k8stesting.DeleteAction); ok {
			del = d
		}
	}
	if del == nil {
		t.Fatalf("no delete action")
	}
	opts := del.GetDeleteOptions()
	if opts.GracePeriodSeconds == nil || *opts.GracePeriodSeconds != 0 {
		t.Errorf("gracePeriodSeconds = %v, want 0", opts.GracePeriodSeconds)
	}
	if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid-1" {
		t.Errorf("preconditions = %+v, want UID uid-1", opts.Preconditions)
	}

	// Deleting twice (kc quits after the shell ended) is not an error.
	if err := nodeshell.Delete(t.Context(), client, pod); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}
