package models

import (
	"os"
	"testing"

	kctesting "github.com/sttts/kc/internal/testing"
	"github.com/sttts/kc/internal/testing/kcenvtest"
	"k8s.io/client-go/rest"
)

// testCfg points at the package-wide envtest control plane. It is nil when
// envtest did not start; tests guard with kcenvtest.Require.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	kctesting.SetupLogging()
	os.Exit(kcenvtest.Main(m, &testCfg))
}
