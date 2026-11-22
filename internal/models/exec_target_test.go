package models

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

func execTestPod(annotation string) *corev1.Pod {
	pod := &corev1.Pod{
		Namespace: "shop", Name: "web-0",
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "migrate"}},
			Containers: []corev1.Container{
				{Name: "proxy"},
				{Name: "app", Stdin: true, TTY: true},
			},
			EphemeralContainers: []corev1.EphemeralContainer{{Name: "debugger", Stdin: true}},
		},
	}
	if annotation != "" {
		pod.Annotations = map[string]string{DefaultContainerAnnotation: annotation}
	}
	return pod
}

func TestPodExecTargetPicksTheDefaultContainer(t *testing.T) {
	t.Parallel()
	deps := Deps{CtxName: "dev", ClusterKey: kccluster.Key{ContextName: "dev"}}
	for _, tc := range []struct {
		name       string
		annotation string
		container  string
		ephemeral  bool
		init       bool
		stdin      bool
	}{
		{name: "no annotation takes the first container", container: "proxy"},
		{name: "annotation names a container", annotation: "app", container: "app", stdin: true},
		{name: "annotation with spaces", annotation: " app ", container: "app", stdin: true},
		{name: "annotation names an ephemeral container", annotation: "debugger", container: "debugger", ephemeral: true, stdin: true},
		{name: "annotation names an init container", annotation: "migrate", container: "migrate", init: true},
		{name: "unknown annotation falls back to the first", annotation: "gone", container: "proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := PodExecTarget(deps, execTestPod(tc.annotation))
			if err != nil {
				t.Fatalf("PodExecTarget: %v", err)
			}
			if got.Container != tc.container || got.Ephemeral != tc.ephemeral || got.Init != tc.init || got.Stdin != tc.stdin {
				t.Fatalf("target = %+v, want container %q ephemeral=%v init=%v stdin=%v", got, tc.container, tc.ephemeral, tc.init, tc.stdin)
			}
			if got.Namespace != "shop" || got.Pod != "web-0" || got.Cluster.ContextName != "dev" {
				t.Fatalf("target identity = %+v", got)
			}
			if !got.Defaulted || !reflect.DeepEqual(got.Containers, []string{"proxy", "app"}) {
				t.Fatalf("defaulted=%v containers=%v, want true [proxy app]", got.Defaulted, got.Containers)
			}
		})
	}
}

func TestPodExecTargetWithoutContainers(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{Namespace: "shop", Name: "empty"}
	if _, err := PodExecTarget(Deps{}, pod); err == nil || !strings.Contains(err.Error(), "no containers") {
		t.Fatalf("err = %v, want a no-containers error", err)
	}
}

func TestExecTargetKubectlArgs(t *testing.T) {
	t.Parallel()
	shell := []string{"--", "sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
	plain := ExecTarget{
		Cluster:   kccluster.Key{ContextName: "dev"},
		Namespace: "shop", Pod: "web-0", Container: "app",
	}
	impersonated := plain
	impersonated.Cluster = kccluster.Key{
		KubeconfigPath:       "/home/u/.kube/config",
		ContextName:          "prod",
		ImpersonateUser:      "alice",
		ImpersonateGroupsKey: "devs,ops",
		ImpersonateUID:       "u-1",
	}
	for _, tc := range []struct {
		name   string
		target ExecTarget
		mode   ExecMode
		want   []string
	}{
		{
			name:   "shell names the context and falls back to sh",
			target: plain,
			mode:   ExecShell,
			want:   append([]string{"--context", "dev", "exec", "-it", "web-0", "-n", "shop", "-c", "app"}, shell...),
		},
		{
			name:   "shell keeps kubeconfig and impersonation of the panel",
			target: impersonated,
			mode:   ExecShell,
			want: append([]string{
				"--kubeconfig", "/home/u/.kube/config", "--context", "prod",
				"--as", "alice", "--as-group", "devs", "--as-group", "ops", "--as-uid", "u-1",
				"exec", "-it", "web-0", "-n", "shop", "-c", "app",
			}, shell...),
		},
		{
			name:   "attach without stdin or tty only streams",
			target: plain,
			mode:   ExecAttach,
			want:   []string{"--context", "dev", "attach", "web-0", "-n", "shop", "-c", "app"},
		},
		{
			name:   "attach with stdin and tty",
			target: withStdio(plain, true, true),
			mode:   ExecAttach,
			want:   []string{"--context", "dev", "attach", "-it", "web-0", "-n", "shop", "-c", "app"},
		},
		{
			name:   "attach with stdin only",
			target: withStdio(plain, true, false),
			mode:   ExecAttach,
			want:   []string{"--context", "dev", "attach", "-i", "web-0", "-n", "shop", "-c", "app"},
		},
		{
			name:   "attach with tty only",
			target: withStdio(plain, false, true),
			mode:   ExecAttach,
			want:   []string{"--context", "dev", "attach", "-t", "web-0", "-n", "shop", "-c", "app"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.target.KubectlArgs(tc.mode); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("KubectlArgs =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func withStdio(t ExecTarget, stdin, tty bool) ExecTarget {
	t.Stdin, t.TTY = stdin, tty
	return t
}

func TestExecTargetCommand(t *testing.T) {
	t.Parallel()
	target := ExecTarget{
		Cluster:        kccluster.Key{KubeconfigPath: "/k", ContextName: "dev"},
		KubectlContext: "dev",
		Namespace:      "shop", Pod: "web-0", Container: "app",
	}
	const script = `'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`
	if got, want := target.Command(ExecShell), "kubectl exec -it web-0 -n shop -c app -- sh -c "+script; got != want {
		t.Fatalf("same context:\n got %s\nwant %s", got, want)
	}
	other := target
	other.KubectlContext = "prod"
	other.Cluster.ImpersonateUser = "system:serviceaccount:shop:viewer"
	if got, want := other.Command(ExecAttach), "kubectl --context dev --as system:serviceaccount:shop:viewer attach web-0 -n shop -c app"; got != want {
		t.Fatalf("other context:\n got %s\nwant %s", got, want)
	}
}

func TestSupportsExec(t *testing.T) {
	t.Parallel()
	deploy := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	container := NewContainerItem("app", []string{"/app"}, nil, nil, nil, nil)
	for _, tc := range []struct {
		name string
		item Item
		want bool
	}{
		{"pod row", NewObjectRow("p", []string{"p"}, nil, podGVR, "shop", "p", nil), true},
		{"deployment row", NewObjectRow("d", []string{"d"}, nil, deploy, "shop", "d", nil), false},
		{"container row", container, true},
		{"plain row", NewSimpleItem("x", []string{"x"}, nil, nil), false},
	} {
		if got := SupportsExec(tc.item); got != tc.want {
			t.Errorf("%s: SupportsExec = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A container row without a target refuses to resolve.
	if _, err := ResolveExecTarget(t.Context(), nil, container); !errors.Is(err, ErrNoExecTarget) {
		t.Fatalf("container without target: err = %v, want ErrNoExecTarget", err)
	}
	if _, err := ResolveExecTarget(t.Context(), nil, NewObjectRow("d", []string{"d"}, nil, deploy, "shop", "d", nil)); !errors.Is(err, ErrNoExecTarget) {
		t.Fatalf("deployment: err = %v, want ErrNoExecTarget", err)
	}
}

func TestContainerFoldersCarryExecTargets(t *testing.T) {
	t.Parallel()
	deps := Deps{CtxName: "dev", KubectlContext: "dev", ClusterKey: kccluster.Key{ContextName: "dev", ImpersonateUser: "alice"}, AppConfig: appconfig.Default()}
	list := &PodContainerListFolder{BaseFolder: NewBaseFolder(deps, nil, []string{"pods", "web-0"}), Namespace: "shop", Pod: "web-0", Kind: containerKindEphemeral}
	records := list.extractContainers(execTestPod(""))
	if len(records) != 1 || records[0].Name != "debugger" || !records[0].Stdin {
		t.Fatalf("ephemeral records = %+v", records)
	}

	target := containerExecTarget(deps, "shop", "web-0", "debugger", containerKindEphemeral, true, true)
	detail := NewPodContainerDetailFolder(deps, []string{"pods", "web-0", "debugger"}, "shop", "web-0", "debugger", containerKindEphemeral)
	detail.execTarget = &target
	rows, err := detail.buildRows(t.Context())
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	var shell ExecLauncher
	for _, r := range rows {
		if l, ok := r.(ExecLauncher); ok {
			shell = l
		}
	}
	if shell == nil {
		t.Fatalf("detail folder has no shell row")
	}
	got, err := shell.ExecTarget(t.Context())
	if err != nil {
		t.Fatalf("ExecTarget: %v", err)
	}
	if shell.ExecMode() != ExecShell || got.Container != "debugger" || !got.Ephemeral || !got.TTY || got.Cluster.ImpersonateUser != "alice" {
		t.Fatalf("shell row target = %+v mode %v", got, shell.ExecMode())
	}
	if cmd := shell.(interface{ ViewCommand() string }).ViewCommand(); !strings.HasPrefix(cmd, "kubectl --as alice exec -it web-0 -n shop -c debugger -- sh -c ") {
		t.Fatalf("shell row command = %q", cmd)
	}
}
