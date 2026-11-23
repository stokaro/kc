package appconfig

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MetricsHistoryConfig lets the Info panel mode read the past CPU and memory
// usage of a workload from a Prometheus-compatible server in the cluster.
// It is off until a backend matches the context.
type MetricsHistoryConfig struct {
	// Window is how far back the history goes. Zero means
	// DefaultMetricsHistoryWindow.
	Window metav1.Duration `json:"window"`
	// Timeout bounds one read of the history (all its queries). Zero means
	// DefaultMetricsHistoryTimeout.
	Timeout metav1.Duration `json:"timeout"`
	// Backends name the Prometheus service per context; the first entry
	// whose Contexts match wins. Empty turns the history off.
	Backends []MetricsBackendConfig `json:"backends"`
}

// MetricsBackendConfig is a Prometheus-compatible service reached through
// the API server's service proxy, with the kubeconfig credentials of the
// context: kc holds no credential of its own for it.
type MetricsBackendConfig struct {
	// Contexts lists context names or globs, as readOnly.contexts does.
	Contexts []string `json:"contexts"`
	// Namespace and Service name the Service in front of Prometheus.
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	// Port is the service port, by number or name. Empty means the
	// service's only port.
	Port string `json:"port,omitempty"`
	// Scheme is http or https, the scheme the proxy uses to reach the
	// service. Empty means http.
	Scheme string `json:"scheme,omitempty"`
	// PathPrefix is put before /api/v1 (Prometheus --web.route-prefix, or
	// /prometheus for Mimir).
	PathPrefix string `json:"pathPrefix,omitempty"`
	// Labels are added as exact matchers to every series kc asks for, for
	// example cluster: prod-eu on a server that holds several clusters. kc
	// never derives them from the context name.
	Labels map[string]string `json:"labels,omitempty"`
}

// Defaults of metricsHistory.
const (
	DefaultMetricsHistoryWindow  = time.Hour
	DefaultMetricsHistoryTimeout = 15 * time.Second
	// MaxMetricsHistoryWindow bounds the window: a longer one makes the
	// workload join expensive on the server for little gain in a sparkline.
	MaxMetricsHistoryWindow = 7 * 24 * time.Hour
)

// EffectiveWindow returns Window within its bounds, or the default.
func (c MetricsHistoryConfig) EffectiveWindow() time.Duration {
	switch w := c.Window.Duration; {
	case w <= 0:
		return DefaultMetricsHistoryWindow
	case w < 5*time.Minute:
		return 5 * time.Minute
	case w > MaxMetricsHistoryWindow:
		return MaxMetricsHistoryWindow
	default:
		return w
	}
}

// EffectiveTimeout returns Timeout, or the default when it is not positive.
func (c MetricsHistoryConfig) EffectiveTimeout() time.Duration {
	if c.Timeout.Duration <= 0 {
		return DefaultMetricsHistoryTimeout
	}
	return c.Timeout.Duration
}

// BackendFor returns the first backend whose Contexts match context.
func (c MetricsHistoryConfig) BackendFor(context string) (MetricsBackendConfig, bool) {
	if context == "" {
		return MetricsBackendConfig{}, false
	}
	for _, b := range c.Backends {
		for _, pattern := range b.Contexts {
			pattern = strings.TrimSpace(pattern)
			if pattern != "" && MatchContext(pattern, context) {
				return b, true
			}
		}
	}
	return MetricsBackendConfig{}, false
}

// DefaultMetricsHistory returns the default: a one hour window and no
// backend.
func DefaultMetricsHistory() MetricsHistoryConfig {
	return MetricsHistoryConfig{
		Window:   metav1.Duration{Duration: DefaultMetricsHistoryWindow},
		Timeout:  metav1.Duration{Duration: DefaultMetricsHistoryTimeout},
		Backends: make([]MetricsBackendConfig, 0),
	}
}
