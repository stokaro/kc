package models

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// /diagnosis inside a claim and inside the pod that uses it read the
// StorageClass through the scoped cache: with WaitForFirstConsumer and an
// unscheduled pod, waiting is expected and explained as such. envtest runs
// no PV controller, so the claim stays Pending.
func TestClaimDiagnosisEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = storagev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("claim-diag")
	className := ns + "-wfc"
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	wfc := storagev1.VolumeBindingWaitForFirstConsumer
	sc := &storagev1.StorageClass{Name: className, Provisioner: "example.com/csi", VolumeBindingMode: &wfc}
	mustCreate(t, cli, sc)
	t.Cleanup(func() { _ = cli.Delete(t.Context(), sc) })
	mustCreate(t, cli, &corev1.PersistentVolumeClaim{Namespace: ns, Name: "data", Spec: corev1.PersistentVolumeClaimSpec{
		StorageClassName: &className,
		AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
	}})
	mustCreate(t, cli, &corev1.Pod{Namespace: ns, Name: "db-0", Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "db", Image: "busybox"}},
		Volumes:    []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}},
	}})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	for _, tc := range []struct {
		gvr        string
		name, want string
	}{
		{"persistentvolumeclaims", "data", "finding/storage/db-0/data/wait-for-consumer"},
		{"pods", "db-0", "finding/storage/db-0/data/wait-for-consumer"},
	} {
		gvr := pvcGVR
		if tc.gvr == "pods" {
			gvr = podGVR
		}
		rows, ok := builtinObjectRows(gvr)
		if !ok {
			t.Fatalf("%s have no object rows", tc.gvr)
		}
		var diag Enterable
		for _, r := range rows(deps, ns, tc.name, []string{tc.gvr, tc.name}) {
			if id, _, _, _ := r.Columns(); strings.HasSuffix(id, "/"+DiagnosisSegment) {
				diag, _ = r.(Enterable)
			}
		}
		if diag == nil {
			t.Fatalf("no /diagnosis row inside %s %s", tc.gvr, tc.name)
		}
		folder, err := diag.Enter()
		if err != nil {
			t.Fatalf("enter: %v", err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			folder.Len(ctx)
			if _, r, ok := folder.Find(ctx, tc.want); ok {
				_, cells, _, _ := r.Columns()
				if len(cells) < 4 || !strings.Contains(cells[3], "WaitForFirstConsumer") {
					t.Fatalf("%s: row %q", tc.gvr, cells)
				}
				break
			}
			if time.Now().After(deadline) {
				var ids []string
				for _, row := range folder.Lines(ctx, 0, folder.Len(ctx)) {
					id, _, _, _ := row.Columns()
					ids = append(ids, id)
				}
				t.Fatalf("%s: no %s row; rows %v", tc.gvr, tc.want, ids)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// The class is cited by its full GVR.
		if _, _, ok := folder.Find(ctx, tc.want+"/storage.k8s.io/v1/storageclasses//"+className); !ok {
			t.Errorf("%s: the finding does not cite the StorageClass", tc.gvr)
		}
		folder.(interface{ Close() }).Close()
	}
}
