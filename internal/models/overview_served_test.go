package models

import (
	"testing"
	"testing/synctest"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// certManagerNotInstalled is the reason of the certificate row on a cluster
// that does not serve cert-manager's Certificates.
const certManagerNotInstalled = "cert-manager is not installed (certificates.cert-manager.io is not served by this cluster)"

func (c *fakeOverviewConn) rediscoverCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rediscovers
}

// TestOverviewRereadRediscovers checks that C-r has the connection read API
// discovery again once, on the next tick, while informer events and the
// periodic recompute do not.
func TestOverviewRereadRediscovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, conn := newTestOverviewEngine(t, Deps{}, AllNamespaces)
		defer e.close()
		_, _ = e.snapshot()
		synctest.Wait()
		conn.fire()
		time.Sleep(overviewRefreshEvery)
		_, _ = e.snapshot()
		synctest.Wait()
		if got := conn.rediscoverCount(); got != 0 {
			t.Fatalf("rediscovers after events and a periodic recompute = %d, want 0", got)
		}
		before := e.computeCount()
		e.reread()
		time.Sleep(overviewInterval)
		synctest.Wait()
		if got := conn.rediscoverCount(); got != 1 {
			t.Fatalf("rediscovers after C-r = %d, want 1", got)
		}
		if got := e.computeCount(); got != before+1 {
			t.Fatalf("recomputes after C-r = %d, want %d", got, before+1)
		}
		time.Sleep(5 * overviewInterval)
		synctest.Wait()
		if got := conn.rediscoverCount(); got != 1 {
			t.Fatalf("rediscovers some ticks after C-r = %d, want still 1", got)
		}
	})
}

// TestOverviewAddonRemovedForgetsItsTargets checks the certificate row
// across cert-manager's removal: the Certificates went with the add-on, so
// the row is not applicable with the add-on's reason and lists none of them
// as recovered, and after a new install the row does not bring them back.
func TestOverviewAddonRemovedForgetsItsTargets(t *testing.T) {
	t.Parallel()
	life := newOverviewLifecycle()
	now := overviewTestNow
	served := func(items ...*unstructured.Unstructured) *overviewInput {
		in := &overviewInput{namespace: AllNamespaces}
		for _, u := range items {
			in.certificates = append(in.certificates, certificatesFrom([]unstructured.Unstructured{*u})...)
		}
		return in
	}

	f := mustFinding(t, lifeCompute(served(certificate("certs", "broken", "False", "Failed", "")), now, nil, life), "risks/certificates")
	if got := targetKeys(f); len(got) != 1 || got[0] != "certificates:certs/broken" {
		t.Fatalf("installed: targets %v, want the broken certificate", got)
	}

	removed := &overviewInput{namespace: AllNamespaces, missing: map[schema.GroupVersionResource]string{ovCertificatesGVR: notServedReason}}
	f = mustFinding(t, lifeCompute(removed, now.Add(time.Minute), nil, life), "risks/certificates")
	if f.notApplicable != certManagerNotInstalled || f.value != "none" || len(f.targets) != 0 {
		t.Fatalf("removed: value %q, n/a %q, targets %v; want none, %q, no targets", f.value, f.notApplicable, targetKeys(f), certManagerNotInstalled)
	}

	f = mustFinding(t, lifeCompute(served(), now.Add(2*time.Minute), nil, life), "risks/certificates")
	if len(f.targets) != 0 || f.notApplicable == "" {
		t.Fatalf("installed again without certificates: value %q, n/a %q, targets %v; want n/a and no recovered target", f.value, f.notApplicable, targetKeys(f))
	}
}
