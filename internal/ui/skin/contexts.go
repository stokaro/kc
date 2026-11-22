package skin

import (
	"errors"
	"fmt"
	"image/color"
	"regexp"
	"strings"

	"github.com/stokaro/kc/pkg/appconfig"
)

// ContextColors maps kubeconfig context names to a frame color. The zero
// value matches nothing.
type ContextColors struct {
	rules []contextRule
}

type contextRule struct {
	re    *regexp.Regexp
	color color.Color
}

// CompileContextColors checks and compiles the contexts config. Bad entries
// are reported, all of them; the good ones still work.
func CompileContextColors(cfg []appconfig.ContextColorConfig) (ContextColors, error) {
	var out ContextColors
	var errs []error
	for i, c := range cfg {
		where := fmt.Sprintf("contexts[%d]", i)
		match, rx := strings.TrimSpace(c.Match), strings.TrimSpace(c.Regex)
		var re *regexp.Regexp
		switch {
		case match != "" && rx != "":
			errs = append(errs, fmt.Errorf("%s: set match or regex, not both", where))
			continue
		case match != "":
			re = globRegexp(match)
		case rx != "":
			var err error
			if re, err = regexp.Compile(rx); err != nil {
				errs = append(errs, fmt.Errorf("%s: regex: %w", where, err))
				continue
			}
		default:
			errs = append(errs, fmt.Errorf("%s: set match or regex", where))
			continue
		}
		col, err := ParseColor(c.Color)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		out.rules = append(out.rules, contextRule{re: re, color: col})
	}
	return out, errors.Join(errs...)
}

// For returns the color of the first rule that matches the context name.
func (c ContextColors) For(context string) (color.Color, bool) {
	if context == "" {
		return nil, false
	}
	for _, r := range c.rules {
		if r.re.MatchString(context) {
			return r.color, true
		}
	}
	return nil, false
}

// globRegexp turns a glob into an anchored regexp. Context names often hold
// "/" (EKS ARNs, OpenShift users), so "*" crosses it, unlike path.Match.
func globRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
