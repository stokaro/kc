package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// certificate returns a cert-manager Certificate with a Ready condition
// (none when ready is "") and status.notAfter (none when notAfter is "").
func certificate(ns, name, ready, reason, notAfter string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(ovCertificateGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	status := map[string]any{}
	if ready != "" {
		status["conditions"] = []any{
			map[string]any{"type": "Issuing", "status": "True"},
			map[string]any{"type": "Ready", "status": ready, "reason": reason, "message": "first line\nsecond line"},
		}
	}
	if notAfter != "" {
		status["notAfter"] = notAfter
	}
	u.Object["status"] = status
	return u
}

func fromNow(d time.Duration) string { return overviewTestNow.Add(d).Format(time.RFC3339) }

func TestCertificateTarget(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	warn := 14 * day
	tests := []struct {
		name   string
		cert   *unstructured.Unstructured
		listed bool
		sev    overviewSeverity
		value  string
	}{
		{name: "ready and far from expiry", cert: certificate("a", "c", "True", "Ready", fromNow(60*day))},
		{name: "ready without notAfter", cert: certificate("a", "c", "True", "Ready", "")},
		{name: "ready, expires just after the threshold", cert: certificate("a", "c", "True", "Ready", fromNow(warn+time.Minute))},
		{name: "ready, expires inside the threshold", cert: certificate("a", "c", "True", "Ready", fromNow(5*day)), listed: true, sev: severityWarning, value: "expires in 5d"},
		{name: "expired", cert: certificate("a", "c", "True", "Ready", fromNow(-2*day)), listed: true, sev: severityFailed, value: "expired 2d ago"},
		{name: "not ready", cert: certificate("a", "c", "False", "Failed", fromNow(60*day)), listed: true, sev: severityFailed, value: "Ready=False Failed: first line"},
		{name: "not ready and expiring", cert: certificate("a", "c", "False", "Issuing", fromNow(3*day)), listed: true, sev: severityFailed, value: "Ready=False Issuing: first line, expires in 3d"},
		{name: "ready unknown", cert: certificate("a", "c", "Unknown", "Pending", ""), listed: true, sev: severityWarning, value: "Ready=Unknown Pending: first line"},
		{name: "no Ready condition", cert: certificate("a", "c", "", "", ""), listed: true, sev: severityWarning, value: "no Ready condition"},
		{name: "notAfter is not a time", cert: certificate("a", "c", "True", "Ready", "soon"), listed: true, sev: severityWarning, value: "notAfter soon is not a time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			infos := certificatesFrom([]unstructured.Unstructured{*tt.cert})
			got, listed := certificateTarget(&infos[0], overviewTestNow, warn)
			if listed != tt.listed {
				t.Fatalf("listed = %v (%+v), want %v", listed, got, tt.listed)
			}
			if !listed {
				return
			}
			if got.severity != tt.sev || got.value != tt.value {
				t.Fatalf("target = %v %q, want %v %q", got.severity, got.value, tt.sev, tt.value)
			}
			if got.gvr != ovCertificatesGVR || got.namespace != "a" || got.name != "c" {
				t.Fatalf("target identity = %v %s/%s", got.gvr, got.namespace, got.name)
			}
		})
	}
}

func TestOverviewCertificatesRule(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	objs := []crclient.Object{
		certificate("a", "fine", "True", "Ready", fromNow(60*day)),
		certificate("a", "soon", "True", "Ready", fromNow(10*day)),
		certificate("a", "sooner", "True", "Ready", fromNow(2*day)),
		certificate("a", "broken", "False", "Failed", fromNow(40*day)),
		certificate("b", "expired", "False", "Expired", fromNow(-day)),
	}
	t.Run("all namespaces, failed first, then the soonest expiry", func(t *testing.T) {
		t.Parallel()
		f := mustFinding(t, reportFor(t, AllNamespaces, appconfig.DefaultOverview(), objs...), "risks/certificates")
		want := []string{"certificates:b/expired", "certificates:a/broken", "certificates:a/sooner", "certificates:a/soon"}
		if got := targetKeys(f); !slices.Equal(got, want) {
			t.Fatalf("targets = %v, want %v", got, want)
		}
		if f.value != "4" || f.severity != severityFailed || f.section != overviewRisks {
			t.Fatalf("row = %q %v %s, want 4 failed in Risks", f.value, f.severity, f.section)
		}
	})
	t.Run("namespace scope", func(t *testing.T) {
		t.Parallel()
		f := mustFinding(t, reportFor(t, "b", appconfig.DefaultOverview(), objs...), "risks/certificates")
		if got := targetKeys(f); !slices.Equal(got, []string{"certificates:b/expired"}) {
			t.Fatalf("targets = %v", got)
		}
		if !strings.Contains(strings.Join(f.kubectl[0], " "), "-n b") {
			t.Fatalf("kubectl = %v, want the namespace", f.kubectl[0])
		}
	})
	t.Run("threshold from config", func(t *testing.T) {
		t.Parallel()
		cfg := appconfig.DefaultOverview()
		cfg.CertificateExpiryDays = 3
		f := mustFinding(t, reportFor(t, "a", cfg, objs...), "risks/certificates")
		want := []string{"certificates:a/broken", "certificates:a/sooner"}
		if got := targetKeys(f); !slices.Equal(got, want) {
			t.Fatalf("targets = %v, want %v", got, want)
		}
		if !strings.Contains(f.explain, "(3) days") {
			t.Fatalf("explain does not name the threshold:\n%s", f.explain)
		}
	})
}

// TestOverviewCertificatesNotServed checks that a cluster without
// cert-manager dims the row with the reason and leaves the others alone.
func TestOverviewCertificatesNotServed(t *testing.T) {
	t.Parallel()
	in := loadOverviewInput(t.Context(), newOverviewFakeClient(), AllNamespaces, func(gvr schema.GroupVersionResource) string {
		if gvr == ovCertificatesGVR {
			return "not served by this cluster"
		}
		return ""
	})
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
	f := mustFinding(t, r, "risks/certificates")
	if f.severity != severityUnknown || f.value != "n/a" || f.unavailable != "certificates.cert-manager.io: not served by this cluster" {
		t.Fatalf("row = %q %v %q, want n/a with the reason", f.value, f.severity, f.unavailable)
	}
	if f := mustFinding(t, r, "risks/quotas"); f.severity != severityOK {
		t.Fatalf("a row that does not need certificates is %v, want ok", f.severity)
	}
}
