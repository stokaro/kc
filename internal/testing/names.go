package kctesting

import (
	utilrand "k8s.io/apimachinery/pkg/util/rand"
)

// maxNameLen is the length of a DNS label, the strictest name rule a test
// object meets (namespaces).
const maxNameLen = 63

// UniqueName returns base followed by a dash and five random characters.
// Tests that share one envtest control plane name their namespaces and other
// cluster-scoped objects with it, so that neither parallel tests nor repeated
// runs (-count) meet an object left behind by another test. envtest runs no
// namespace controller: a deleted namespace stays Terminating, and creating
// objects in it fails.
func UniqueName(base string) string {
	const suffix = 6 // "-" plus 5 characters
	if len(base) > maxNameLen-suffix {
		base = base[:maxNameLen-suffix]
	}
	return base + "-" + utilrand.String(suffix-1)
}
