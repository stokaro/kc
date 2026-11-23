package models

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// certificateInfo is what the overview reads from a cert-manager
// Certificate: its Ready condition and the expiry of the issued
// certificate.
type certificateInfo struct {
	meta metav1.ObjectMeta // namespace and name only
	// ready is the status of the Ready condition: True, False, Unknown, or
	// "" when the condition is missing.
	ready          string
	reason, detail string // reason and message of the Ready condition
	notAfter       time.Time
	// badNotAfter holds status.notAfter when it is set but not a time.
	badNotAfter string
}

// certificatesFrom reads the fields the overview needs. The items come from
// the cache and are not modified.
func certificatesFrom(items []unstructured.Unstructured) []certificateInfo {
	out := make([]certificateInfo, 0, len(items))
	for i := range items {
		u := &items[i]
		c := certificateInfo{meta: metav1.ObjectMeta{Namespace: u.GetNamespace(), Name: u.GetName()}}
		if s, ok, _ := unstructured.NestedString(u.Object, "status", "notAfter"); ok && s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				c.notAfter = t
			} else {
				c.badNotAfter = s
			}
		}
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, raw := range conds {
			m, ok := raw.(map[string]any)
			if !ok || m["type"] != "Ready" {
				continue
			}
			c.ready, _ = m["status"].(string)
			c.reason, _ = m["reason"].(string)
			c.detail, _ = m["message"].(string)
			break
		}
		out = append(out, c)
	}
	return out
}

// certificatesRule lists Certificates that are not Ready, or whose issued
// certificate expires within certificateExpiryDays.
func certificatesRule(rc *ruleContext) overviewFinding {
	days := rc.cfg.CertificateExpiryDays
	f := overviewFinding{
		id: "risks/certificates", section: overviewRisks, title: "/certificates not ready or expiring",
		explain: fmt.Sprintf(`cert-manager Certificates (certificates.cert-manager.io/v1) whose Ready
condition is False (failed), Unknown or missing (warning), or whose
status.notAfter is less than certificateExpiryDays (%d) days away (warning)
or past (failed). cert-manager renews a certificate well before it expires
(by default when two thirds of its lifetime have passed), so a certificate
this close to its expiry usually means that renewal fails. Enter lists the
certificates, the soonest expiry first; kubectl describe on one shows the
CertificateRequest and the issuer's error.

The row is not applicable (n/a, not a failure) when the cluster does not
serve the kind, which means cert-manager is not installed. Any other reason
the kind cannot be read, such as forbidden, makes it unavailable.`, days),
		kubectl: [][]string{
			kubectlGet("certificates.cert-manager.io", append(rc.nsArgs(), "-o",
				`custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,REASON:.status.conditions[?(@.type=="Ready")].reason,NOT_AFTER:.status.notAfter`)...),
			append([]string{"describe", "certificates.cert-manager.io"}, rc.nsArgs()...),
		},
		supplies: []string{
			"the Ready condition and notAfter of each certificate",
			"the conditions and events of each certificate",
		},
	}
	warnBefore := time.Duration(days) * 24 * time.Hour
	return countFinding(rc, f, []schema.GroupVersionResource{ovCertificatesGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.certificates {
			if t, ok := certificateTarget(&rc.in.certificates[i], rc.now, warnBefore); ok {
				out = append(out, t)
			}
		}
		return out
	})
}

// certificateTarget classifies one Certificate. ok is false when it is
// Ready and does not expire within warnBefore.
func certificateTarget(c *certificateInfo, now time.Time, warnBefore time.Duration) (overviewTarget, bool) {
	sev := severityOK
	var parts []string
	raise := func(s overviewSeverity, part string) {
		sev = max(sev, s)
		parts = append(parts, part)
	}
	switch c.ready {
	case "True":
	case "False":
		raise(severityFailed, readyText(c))
	case "":
		raise(severityWarning, "no Ready condition")
	default:
		raise(severityWarning, readyText(c))
	}
	rank := 0.0
	switch {
	case c.badNotAfter != "":
		raise(severityWarning, "notAfter "+c.badNotAfter+" is not a time")
	case !c.notAfter.IsZero():
		left := c.notAfter.Sub(now)
		// The sooner a certificate expires, the higher it is listed.
		rank = -left.Hours()
		switch {
		case left <= 0:
			raise(severityFailed, "expired "+shortDuration(-left)+" ago")
		case left < warnBefore:
			raise(severityWarning, "expires in "+shortDuration(left))
		}
	}
	if sev == severityOK {
		return overviewTarget{}, false
	}
	return overviewTarget{
		gvr: ovCertificatesGVR, namespace: c.meta.Namespace, name: c.meta.Name,
		severity: sev, rank: rank, value: strings.Join(parts, ", "),
	}, true
}

// readyText is "Ready=False Issuing: <first line of the message>".
func readyText(c *certificateInfo) string {
	s := "Ready=" + c.ready
	if c.reason != "" {
		s += " " + c.reason
	}
	if msg, _, _ := strings.Cut(strings.TrimSpace(c.detail), "\n"); msg != "" {
		s += ": " + msg
	}
	return s
}
