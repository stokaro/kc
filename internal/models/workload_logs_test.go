package models

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestWorkloadLogsCommand(t *testing.T) {
	t.Parallel()
	deploy := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	tests := []struct {
		name     string
		deps     Deps
		spec     WorkloadLogsSpec
		logs     *LogsSpec // nil: spec.LogsSpec()
		selector string
		max      int
		want     string
	}{
		{
			name:     "workload selector",
			spec:     WorkloadLogsSpec{Namespace: "web", Workload: deploy, WorkloadName: "api", Follow: true, TailLines: 200},
			selector: "app=api",
			max:      50,
			want:     "kubectl logs -l app=api -n web --all-containers --prefix --tail=200 --max-log-requests=50 -f",
		},
		{
			name:     "set-based selector is quoted, zero tail is the default",
			spec:     WorkloadLogsSpec{Namespace: "web", Workload: deploy, WorkloadName: "api", Follow: true},
			selector: "tier in (a,b)",
			max:      5,
			want:     "kubectl logs -l 'tier in (a,b)' -n web --all-containers --prefix --tail=200 --max-log-requests=5 -f",
		},
		{
			name: "one pod in another context",
			deps: Deps{CtxName: "prod", KubectlContext: "dev"},
			spec: WorkloadLogsSpec{Namespace: "web", Pod: "api-1", Follow: true, TailLines: 50},
			max:  50,
			want: "kubectl --context prod logs api-1 -n web --all-containers --prefix --tail=50 --max-log-requests=50 -f",
		},
		{
			name:     "viewer options: whole log, since, timestamps, previous",
			spec:     WorkloadLogsSpec{Namespace: "web", Workload: deploy, WorkloadName: "api", Follow: true},
			logs:     &LogsSpec{Namespace: "web", Follow: true, TailLines: -1, SinceSeconds: 900, Timestamps: true, Previous: true},
			selector: "app=api",
			max:      50,
			want:     "kubectl logs -l app=api -n web --all-containers --prefix --tail=-1 --since=15m --timestamps -p --max-log-requests=50 -f",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logs := tc.spec.LogsSpec()
			if tc.logs != nil {
				logs = *tc.logs
			}
			if got := workloadLogsCommand(tc.deps, tc.spec, logs, tc.selector, tc.max); got != tc.want {
				t.Fatalf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestWorkloadLogsRowOnlyForWorkloads(t *testing.T) {
	t.Parallel()
	deps := Deps{AppConfig: appconfig.Default()}
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	tests := []struct {
		parent schema.GroupVersionResource
		want   bool
	}{
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, true},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, true},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, true},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, true},
		{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, true},
		{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, false},
		{schema.GroupVersionResource{Version: "v1", Resource: "services"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.parent.Resource, func(t *testing.T) {
			t.Parallel()
			path := []string{"namespaces", "web", tc.parent.Resource, "api"}
			folder := NewChildResourceTypesFolder(deps, tc.parent, "web", "api", []schema.GroupVersionResource{pods}, path)
			rows, err := folder.populate(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var found *WorkloadLogItem
			for _, r := range rows {
				if it, ok := r.(*WorkloadLogItem); ok {
					found = it
				}
			}
			if (found != nil) != tc.want {
				t.Fatalf("logs row present = %v, want %v", found != nil, tc.want)
			}
			if found == nil {
				return
			}
			want := WorkloadLogsSpec{Namespace: "web", Workload: tc.parent, WorkloadName: "api", Follow: true, TailLines: DefaultLogsTailLines}
			if spec := found.WorkloadLogsSpec(); spec != want || spec.IsPod() {
				t.Fatalf("spec = %+v, want %+v", spec, want)
			}
		})
	}
}

func TestPodContainersFolderHasAllContainersLogsRow(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{GenerateName: "workload-logs-row-"}
	if err := cli.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		Namespace: ns.Name, Name: "api-1",
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
	}
	if err := cli.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = cl.Start(ctx) }()
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	folder := NewPodContainersFolder(deps, []string{"namespaces", ns.Name, "pods", "api-1"}, ns.Name, "api-1")
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := folder.buildRows(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("%d rows, want /containers, the logs row, /diagnosis and /timeline", len(rows))
	}
	if id, _, _, _ := rows[2].Columns(); id != ns.Name+"/api-1/"+DiagnosisSegment {
		t.Fatalf("last row = %q, want the /diagnosis row", id)
	}
	last, ok := rows[1].(*WorkloadLogItem)
	if !ok {
		t.Fatalf("second row = %T, want the all-containers logs row", rows[1])
	}
	if _, cells, _, _ := last.Columns(); len(cells) == 0 || cells[0] != "logs (all containers)" {
		t.Fatalf("row cells = %v", cells)
	}
	want := WorkloadLogsSpec{Namespace: ns.Name, Pod: "api-1", Follow: true, TailLines: DefaultLogsTailLines}
	if spec := last.WorkloadLogsSpec(); spec != want || !spec.IsPod() {
		t.Fatalf("spec = %+v, want %+v", spec, want)
	}
	wantCmd := "kubectl logs api-1 -n " + ns.Name + " --all-containers --prefix --tail=200 --max-log-requests=50 -f"
	if got := last.ViewCommand(); got != wantCmd {
		t.Fatalf("view command = %q, want %q", got, wantCmd)
	}
}
