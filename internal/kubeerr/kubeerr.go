// Package kubeerr turns the error of a Kubernetes request into one line of
// plain text for the user. The cause comes first, and the server is named
// by its host instead of the request URL, so the text still says what went
// wrong when a toast or a narrow panel cuts it.
package kubeerr

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Kind groups errors by what the user can do about them.
type Kind int

const (
	// KindOther is any error not listed below.
	KindOther Kind = iota
	// KindUnreachable: the server did not answer (connection refused, a
	// timeout, an unknown host, a dropped connection, 503).
	KindUnreachable
	// KindCredentials: the server rejected the credentials (401).
	KindCredentials
	// KindCertificate: the server's certificate is not trusted.
	KindCertificate
	// KindForbidden: the user may not do this (403).
	KindForbidden
	// KindNamespaceMissing: the namespace does not exist.
	KindNamespaceMissing
)

// Connection reports whether k is a problem with the connection to the
// server rather than with one request: a retry or another context helps,
// not another object.
func (k Kind) Connection() bool {
	switch k {
	case KindUnreachable, KindCredentials, KindCertificate:
		return true
	default:
		return false
	}
}

// KindOf classifies err; nil is KindOther.
func KindOf(err error) Kind {
	switch {
	case err == nil:
		return KindOther
	case namespaceMissing(err) != "":
		return KindNamespaceMissing
	case apierrors.IsForbidden(err):
		return KindForbidden
	case apierrors.IsUnauthorized(err):
		return KindCredentials
	case certificateProblem(err) != "":
		return KindCertificate
	case unreachableReason(err) != "":
		return KindUnreachable
	default:
		return KindOther
	}
}

// Describe is DescribeAt without a server: an error that does not name its
// server says "the API server".
func Describe(err error) string { return DescribeAt(err, "") }

// Cause is Describe for text that follows a colon, as in "Cannot switch
// to staging: cannot reach 10.0.0.1:6443: connection refused": its first
// letter is in lower case.
func Cause(err error) string {
	s := Describe(err)
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}

// DescribeAt returns err as one line of plain text. server is the API
// server URL of the request, used when err does not carry the URL itself
// (a 401 answer, a timeout of the caller's context). It returns "" for a
// nil err.
func DescribeAt(err error, server string) string {
	if err == nil {
		return ""
	}
	host := hostOf(err)
	if host == "" {
		host = hostOfURL(server)
	}
	if ns := namespaceMissing(err); ns != "" {
		return "Namespace " + ns + " does not exist"
	}
	if apierrors.IsForbidden(err) {
		return forbiddenText(err)
	}
	if apierrors.IsUnauthorized(err) {
		return subject(host) + " rejected the credentials (401 Unauthorized)"
	}
	if cert := certificateProblem(err); cert != "" {
		if host == "" {
			return "The certificate of the API server " + cert
		}
		return "The certificate of " + host + " " + cert
	}
	if reason := unreachableReason(err); reason != "" {
		if host == "" {
			return "Cannot reach the API server: " + reason
		}
		return "Cannot reach " + host + ": " + reason
	}
	if ue, ok := errors.AsType[*url.Error](err); ok && ue.Err != nil {
		return "Request to " + subject(host) + " failed: " + oneLine(ue.Err.Error())
	}
	return capitalize(oneLine(err.Error()))
}

// subject names the server at the start of a sentence.
func subject(host string) string {
	if host == "" {
		return "The API server"
	}
	return host
}

// hostOf is the host of the request URL err carries, or "".
func hostOf(err error) string {
	ue, ok := errors.AsType[*url.Error](err)
	if !ok {
		return ""
	}
	return hostOfURL(ue.URL)
}

func hostOfURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// unreachableReason is why the server did not answer, or "" when err is
// not such a failure.
func unreachableReason(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "no route to the host"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "the connection was reset"
	}
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		if dnsErr.IsNotFound {
			return "no such host"
		}
		return "the host name could not be resolved"
	}
	if timedOut(err) {
		return "no answer in time"
	}
	if apierrors.IsServiceUnavailable(err) {
		return "the server is unavailable (503)"
	}
	if _, ok := errors.AsType[*url.Error](err); ok && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return "the server closed the connection"
	}
	return ""
}

func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return true
	}
	netErr, ok := errors.AsType[net.Error](err)
	return ok && netErr.Timeout()
}

// certificateProblem says what is wrong with the server's certificate,
// worded to follow "The certificate of <host> ", or "" when err is not a
// certificate failure.
func certificateProblem(err error) string {
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return "is not trusted: signed by an unknown authority"
	}
	if he, ok := errors.AsType[x509.HostnameError](err); ok {
		return "is not valid for " + he.Host
	}
	if ie, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		switch ie.Reason {
		case x509.Expired:
			return "has expired or is not valid yet"
		default:
			return "is not valid: " + oneLine(ie.Error())
		}
	}
	return ""
}

// namespaceMissing is the name of the namespace a NotFound answer is
// about, or "" when err is not one.
func namespaceMissing(err error) string {
	if !apierrors.IsNotFound(err) {
		return ""
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return ""
	}
	details := status.Status().Details
	if details == nil || details.Group != "" || details.Kind != "namespaces" {
		return ""
	}
	return details.Name
}

// rbacDenial matches the RBAC authorizer's message (kubernetes
// plugin/pkg/auth/authorizer/rbac): who, the verb, the resource, its API
// group, and the namespace or the cluster scope.
var rbacDenial = regexp.MustCompile(`User "([^"]*)" cannot (\S+) resource "([^"]*)"(?: in API group "([^"]*)")?(?: in the namespace "([^"]*)"| at the cluster scope)?`)

// forbiddenText says what the user may not do and who the user is, from
// the authorizer's message. Another message is kept after "Not allowed:".
func forbiddenText(err error) string {
	msg := err.Error()
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Message != "" {
		msg = status.Status().Message
	}
	m := rbacDenial.FindStringSubmatch(msg)
	if m == nil {
		if _, reason, ok := strings.Cut(msg, "forbidden: "); ok {
			msg = reason
		}
		return "Not allowed: " + oneLine(msg)
	}
	user, verb, resource, group, namespace := m[1], m[2], m[3], m[4], m[5]
	if group != "" {
		resource += "." + group
	}
	where := " cluster-wide"
	if namespace != "" {
		where = " in " + namespace
	}
	return fmt.Sprintf("Not allowed to %s %s%s (user %s)", verb, resource, where, user)
}

// oneLine joins the lines of s with "; ".
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return strings.Join(fields, "; ")
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
