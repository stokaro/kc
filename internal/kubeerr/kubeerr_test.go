package kubeerr_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/kubeerr"
)

// refusedURL is a URL on a local port that refuses connections.
func refusedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return "https://" + addr + "/api?timeout=32s"
}

// silentURL is a URL on a local port that accepts connections and never
// answers.
func silentURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	return "https://" + l.Addr().String() + "/api"
}

func get(t *testing.T, client *http.Client, rawURL string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("GET %s succeeded", rawURL)
	}
	return err
}

func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// Errors as client-go returns them: a refused port, a server that never
// answers, a certificate the client does not trust or that names another
// host. The text names the host, not the URL with its query.
func TestDescribeRealTransportErrors(t *testing.T) {
	t.Parallel()
	tlsServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsServer.Close)
	tlsHost := hostPort(t, tlsServer.URL)
	pool := x509.NewCertPool()
	pool.AddCert(tlsServer.Certificate())
	wrongName := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "other.test"}}}

	refused := refusedURL(t)
	silent := silentURL(t)
	tests := []struct {
		name string
		err  error
		want string
		kind kubeerr.Kind
	}{
		{"connection refused", get(t, http.DefaultClient, refused), "Cannot reach " + hostPort(t, refused) + ": connection refused", kubeerr.KindUnreachable},
		{"no answer", get(t, &http.Client{Timeout: 100 * time.Millisecond}, silent), "Cannot reach " + hostPort(t, silent) + ": no answer in time", kubeerr.KindUnreachable},
		{"unknown authority", get(t, http.DefaultClient, tlsServer.URL+"/api"), "The certificate of " + tlsHost + " is not trusted: signed by an unknown authority", kubeerr.KindCertificate},
		{"other host name", get(t, wrongName, tlsServer.URL+"/api"), "The certificate of " + tlsHost + " is not valid for other.test", kubeerr.KindCertificate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := kubeerr.Describe(tc.err); got != tc.want {
				t.Errorf("Describe(%q) = %q, want %q", tc.err, got, tc.want)
			}
			if got := kubeerr.KindOf(tc.err); got != tc.kind {
				t.Errorf("KindOf(%q) = %v, want %v", tc.err, got, tc.kind)
			}
			if !tc.kind.Connection() {
				t.Errorf("%v is not a connection problem", tc.kind)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	const server = "https://10.0.0.1:6443"
	configMaps := schema.GroupResource{Resource: "configmaps"}
	rbac := func(gr schema.GroupResource, msg string) error {
		return apierrors.NewForbidden(gr, "", errors.New(msg))
	}
	wrapped := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://api.example.test:443/api/v1/pods?limit=500", Err: err}
	}
	tests := []struct {
		name   string
		err    error
		server string
		want   string
		kind   kubeerr.Kind
	}{
		{
			name: "forbidden in a namespace",
			err:  rbac(configMaps, `User "limited" cannot list resource "configmaps" in API group "" in the namespace "team-a"`),
			want: "Not allowed to list configmaps in team-a (user limited)",
			kind: kubeerr.KindForbidden,
		},
		{
			name: "forbidden with an API group",
			err:  rbac(schema.GroupResource{Group: "apps", Resource: "deployments"}, `User "system:serviceaccount:ci:deployer" cannot watch resource "deployments" in API group "apps" in the namespace "shop"`),
			want: "Not allowed to watch deployments.apps in shop (user system:serviceaccount:ci:deployer)",
			kind: kubeerr.KindForbidden,
		},
		{
			name: "forbidden cluster-wide",
			err:  rbac(schema.GroupResource{Resource: "nodes"}, `User "limited" cannot list resource "nodes" in API group "" at the cluster scope`),
			want: "Not allowed to list nodes cluster-wide (user limited)",
			kind: kubeerr.KindForbidden,
		},
		{
			name: "forbidden by another authorizer",
			err:  rbac(configMaps, "the user may not list and watch configmaps in team-a"),
			want: "Not allowed: the user may not list and watch configmaps in team-a",
			kind: kubeerr.KindForbidden,
		},
		{
			name: "forbidden, wrapped",
			err:  fmt.Errorf("list: %w", rbac(configMaps, `User "u" cannot list resource "configmaps" in API group "" in the namespace "a"`)),
			want: "Not allowed to list configmaps in a (user u)",
			kind: kubeerr.KindForbidden,
		},
		{
			name:   "unauthorized names the server it was sent to",
			err:    apierrors.NewUnauthorized("Unauthorized"),
			server: server,
			want:   "10.0.0.1:6443 rejected the credentials (401 Unauthorized)",
			kind:   kubeerr.KindCredentials,
		},
		{
			name: "unauthorized without a server",
			err:  apierrors.NewUnauthorized("Unauthorized"),
			want: "The API server rejected the credentials (401 Unauthorized)",
			kind: kubeerr.KindCredentials,
		},
		{
			name:   "expired certificate",
			err:    wrapped(&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.Expired}}),
			server: server,
			want:   "The certificate of api.example.test:443 has expired or is not valid yet",
			kind:   kubeerr.KindCertificate,
		},
		{
			name: "unknown host",
			err:  wrapped(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "api.example.test", IsNotFound: true}}),
			want: "Cannot reach api.example.test:443: no such host",
			kind: kubeerr.KindUnreachable,
		},
		{
			name:   "the caller's deadline",
			err:    fmt.Errorf("list pods: %w", context.DeadlineExceeded),
			server: server,
			want:   "Cannot reach 10.0.0.1:6443: no answer in time",
			kind:   kubeerr.KindUnreachable,
		},
		{
			name: "service unavailable",
			err:  apierrors.NewServiceUnavailable("refused by the test proxy"),
			want: "Cannot reach the API server: the server is unavailable (503)",
			kind: kubeerr.KindUnreachable,
		},
		{
			name: "missing namespace",
			err:  apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "shpo"),
			want: "Namespace shpo does not exist",
			kind: kubeerr.KindNamespaceMissing,
		},
		{
			name: "another object not found",
			err:  apierrors.NewNotFound(configMaps, "settings"),
			want: `Configmaps "settings" not found`,
			kind: kubeerr.KindOther,
		},
		{
			name: "another transport failure keeps its cause",
			err:  wrapped(errors.New("getting credentials: exec: executable kubelogin not found")),
			want: "Request to api.example.test:443 failed: getting credentials: exec: executable kubelogin not found",
			kind: kubeerr.KindOther,
		},
		{
			name: "anything else, on one line",
			err:  errors.New("first line\nsecond line"),
			want: "First line; second line",
			kind: kubeerr.KindOther,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := kubeerr.DescribeAt(tc.err, tc.server); got != tc.want {
				t.Errorf("DescribeAt(%q, %q) = %q, want %q", tc.err, tc.server, got, tc.want)
			}
			if got := kubeerr.KindOf(tc.err); got != tc.kind {
				t.Errorf("KindOf(%q) = %v, want %v", tc.err, got, tc.kind)
			}
		})
	}
	if got := kubeerr.Describe(nil); got != "" {
		t.Errorf("Describe(nil) = %q, want empty", got)
	}
}

// The texts this package exists for fit an 80-column toast after a short
// prefix, cause included, where the raw client-go errors did not.
func TestDescribeKeepsTheCauseShort(t *testing.T) {
	t.Parallel()
	refused := refusedURL(t)
	raw := get(t, http.DefaultClient, refused)
	text := "Cannot switch to refused: " + kubeerr.Cause(raw)
	if len(text) > 80 || !strings.HasSuffix(text, "connection refused") {
		t.Errorf("toast text %q (%d bytes) does not end with the cause within 80 columns", text, len(text))
	}
	if strings.Contains(kubeerr.Describe(raw), "?timeout") {
		t.Errorf("text %q repeats the request URL", kubeerr.Describe(raw))
	}
}

func TestCauseStartsInLowerCase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{err: apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "shpo"), want: "namespace shpo does not exist"},
		{err: errors.New("Boom"), want: "boom"},
		{err: nil, want: ""},
	}
	for _, tt := range tests {
		if got := kubeerr.Cause(tt.err); got != tt.want {
			t.Errorf("Cause(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
