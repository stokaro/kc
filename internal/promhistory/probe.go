package promhistory

import (
	"context"
	"errors"
	"time"
)

// ErrForbidden is what errors of a request the API server refused (401 or
// 403 on services/proxy) match with errors.Is.
var ErrForbidden = errors.New("forbidden")

// forbiddenError keeps the message of a refused request and matches
// ErrForbidden.
type forbiddenError struct{ msg string }

func (e forbiddenError) Error() string      { return e.msg }
func (forbiddenError) Is(target error) bool { return target == ErrForbidden }

// Labels returns the configured cluster labels of the client. The map is
// shared; callers must not change it.
func (c *Client) Labels() map[string]string { return c.labels }

// Selector returns a PromQL label selector: the configured labels, then
// the extra matchers (each `name="value"` or another matcher), in braces.
// Values given through Eq are quoted, so they cannot end the selector.
func (c *Client) Selector(extra ...string) string {
	s, err := newSelector(c.labels)
	if err != nil {
		// NewServiceProxyClient refused invalid labels already.
		return "{}"
	}
	return s.with(extra...)
}

// UnlabeledSelector is Selector without the configured labels: it finds
// series that exist on the server but that the label mapping excludes.
func UnlabeledSelector(extra ...string) string {
	return selector{}.with(extra...)
}

// Eq is the matcher name="value", with value quoted as a PromQL string.
func Eq(name, value string) string { return eq(name, value) }

// Scalar runs the instant query expr at at (zero means now) and returns the
// value of its single sample, 0 when the result is empty: count() over
// nothing returns no series.
func (c *Client) Scalar(ctx context.Context, expr string, at time.Time) (float64, []string, error) {
	if at.IsZero() {
		at = time.Now()
	}
	ss, warnings, err := c.query(ctx, expr, at)
	if err != nil {
		return 0, warnings, err
	}
	return scalar(ss), warnings, nil
}
