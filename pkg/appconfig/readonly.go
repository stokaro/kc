package appconfig

import "strings"

// ReadOnlyConfig opens kubeconfig contexts in read-only mode. In read-only
// mode kc refuses edits, deletes, namespace creation, the pod filesystem
// debug helper and custom commands marked dangerous.
type ReadOnlyConfig struct {
	// Enabled makes every context read-only.
	Enabled bool `json:"enabled"`
	// Contexts lists context names or globs that open read-only, for
	// example "*prod*". "*" matches any run of characters (including "/",
	// which EKS ARNs contain), "?" one character; everything else is
	// literal. A pattern must match the whole name, case-sensitively.
	Contexts []string `json:"contexts"`
}

// For reports whether the config makes the named context read-only.
func (c ReadOnlyConfig) For(context string) bool {
	if c.Enabled {
		return true
	}
	if context == "" {
		return false
	}
	for _, pattern := range c.Contexts {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" && MatchContext(pattern, context) {
			return true
		}
	}
	return false
}

// MatchContext matches a kubeconfig context name against a glob: "*"
// matches any run of characters, "?" exactly one, everything else itself.
// Unlike path.Match, "*" crosses "/" and "[" is literal, so names such as
// "arn:aws:eks:eu-west-1:1234:cluster/prod" match "*prod*".
func MatchContext(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]) && p[pi] != '*':
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			// Let the last star take one more character and retry.
			mark++
			pi, ni = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
