package navigation

import (
	"os"
	"testing"

	"k8s.io/client-go/rest"

	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// testCfg points at the package-wide envtest control plane. It is nil when
// envtest did not start; tests guard with kcenvtest.Require. Tests name
// their namespaces and nodes with kctesting.UniqueName, so they can share it.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	kctesting.SetupLogging()
	os.Exit(kcenvtest.Main(m, &testCfg))
}
