package tablecache

import (
	"os"
	"testing"

	kctesting "github.com/sttts/kc/internal/testing"
	"github.com/sttts/kc/internal/testing/kcenvtest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

var (
	// testCfg points at the package-wide envtest control plane. It is nil
	// when envtest did not start; tests guard with kcenvtest.Require.
	testCfg    *rest.Config
	testScheme = runtime.NewScheme()
)

func TestMain(m *testing.M) {
	kctesting.SetupLogging()

	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(metav1.AddMetaToScheme(testScheme))
	utilruntime.Must(AddToScheme(testScheme))

	os.Exit(kcenvtest.Main(m, &testCfg))
}
