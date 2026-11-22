package access

import (
	"context"
	"fmt"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// Result is the answer to one check.
type Result struct {
	Decision Decision
	// Reason explains the decision: the authorizer's reason, the evaluation
	// error, or why the review could not run.
	Reason    string
	CheckedAt time.Time
	// Stale is set by the cache when the result is older than its TTL.
	Stale bool
}

// Reviewer answers a check. Implementations never return an error: a failed
// review is a Result with Decision Unknown and the error as Reason.
type Reviewer interface {
	Review(ctx context.Context, c Check) Result
}

// ReviewerFunc adapts a function to Reviewer.
type ReviewerFunc func(context.Context, Check) Result

// Review implements Reviewer.
func (f ReviewerFunc) Review(ctx context.Context, c Check) Result { return f(ctx, c) }

// SSARReviewer sends a SelfSubjectAccessReview per check. The review is
// evaluated for the user the client authenticates as; with impersonation set
// on the client's rest.Config that is the impersonated user and groups.
type SSARReviewer struct {
	Client authorizationv1client.SelfSubjectAccessReviewsGetter
	// Timeout bounds one review; zero means 10 seconds.
	Timeout time.Duration
	Now     func() time.Time
}

// Review implements Reviewer.
func (r SSARReviewer) Review(ctx context.Context, c Check) Result {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if r.Client == nil {
		return Result{Decision: Unknown, Reason: "no API client", CheckedAt: now()}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log := ctrllog.FromContext(ctx).WithName("access")
	log.Info("creating SelfSubjectAccessReview",
		"verb", c.Verb,
		"group", c.Resource.Group,
		"version", c.Resource.Version,
		"resource", c.Resource.Resource,
		"subresource", c.Subresource,
		"namespace", c.Namespace,
		"name", c.Name,
	)
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace:   c.Namespace,
				Verb:        c.Verb,
				Group:       c.Resource.Group,
				Version:     c.Resource.Version,
				Resource:    c.Resource.Resource,
				Subresource: c.Subresource,
				Name:        c.Name,
			},
		},
	}
	resp, err := r.Client.SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return Result{Decision: Unknown, Reason: reviewErrorReason(err), CheckedAt: now()}
	}
	return resultFromStatus(resp.Status, now())
}

func reviewErrorReason(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "access review not permitted: " + err.Error()
	case apierrors.IsNotFound(err), apierrors.IsMethodNotSupported(err):
		return "access reviews not supported by this server: " + err.Error()
	default:
		return "access review failed: " + err.Error()
	}
}

// resultFromStatus maps the review status. "Not allowed" without an explicit
// denial means no authorizer allowed it, which the API server treats as a
// denial; an evaluation error makes the answer Unknown because some rules
// could not be read.
func resultFromStatus(st authorizationv1.SubjectAccessReviewStatus, at time.Time) Result {
	reason := strings.TrimSpace(st.Reason)
	evalErr := strings.TrimSpace(st.EvaluationError)
	switch {
	case st.Allowed:
		if reason == "" {
			reason = "allowed"
		}
		return Result{Decision: Allowed, Reason: reason, CheckedAt: at}
	case evalErr != "":
		msg := "evaluation error: " + evalErr
		if reason != "" {
			msg = reason + "; " + msg
		}
		return Result{Decision: Unknown, Reason: msg, CheckedAt: at}
	default:
		if reason == "" {
			if st.Denied {
				reason = "denied by an authorizer"
			} else {
				reason = "no RBAC rule or authorizer allows it"
			}
		}
		return Result{Decision: Denied, Reason: reason, CheckedAt: at}
	}
}

// CanICommand returns the kubectl auth can-i command that asks the same
// question as c for identity id.
func CanICommand(id Identity, c Check) string {
	parts := []string{"kubectl"}
	if id.Context != "" && id.Context != id.KubectlCtx {
		parts = append(parts, "--context", shellQuote(id.Context))
	}
	res := c.Resource.Resource
	if c.Resource.Group != "" {
		res += "." + c.Resource.Group
	}
	if c.Name != "" {
		res += "/" + c.Name
	}
	parts = append(parts, "auth", "can-i", shellQuote(c.Verb), shellQuote(res))
	if c.Subresource != "" {
		parts = append(parts, "--subresource", shellQuote(c.Subresource))
	}
	if c.Namespace != "" {
		parts = append(parts, "-n", shellQuote(c.Namespace))
	}
	if u := strings.TrimSpace(id.User); u != "" {
		parts = append(parts, "--as", shellQuote(u))
	}
	if uid := strings.TrimSpace(id.UID); uid != "" {
		parts = append(parts, "--as-uid", shellQuote(uid))
	}
	for _, g := range id.Groups {
		if g = strings.TrimSpace(g); g != "" {
			parts = append(parts, "--as-group", shellQuote(g))
		}
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:=@%+,-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// String renders a check for logs and the access view.
func (c Check) String() string {
	s := fmt.Sprintf("%s %s", c.Verb, c.ResourceString())
	if c.Name != "" {
		s += " " + c.Name
	}
	if c.Namespace != "" {
		s += " -n " + c.Namespace
	}
	return s
}
