package tablecache

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

// A refused table list keeps the server's message, which names the user
// and the verb. Before, the default scheme of New knew no meta types,
// client-go could not decode the Status body, and the error read
// "unknown (get configmaps)".
func TestTableListForbiddenKeepsTheServerMessage(t *testing.T) {
	const message = `configmaps is forbidden: User "limited" cannot list resource "configmaps" in API group "" in the namespace "team-a"`
	status := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "",
		errAuthorizer(`User "limited" cannot list resource "configmaps" in API group "" in the namespace "team-a"`)).Status()
	status.APIVersion, status.Kind = "v1", "Status"
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &rest.Config{Host: "https://example.com"}
	cfg.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp := &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Request:    req,
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})
	fetcher, err := newRESTTableFetcher(cfg, fetcherScheme())
	if err != nil {
		t.Fatal(err)
	}
	mapping := &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Version: "v1", Resource: "configmaps"},
		GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"},
		Scope:            meta.RESTScopeNamespace,
	}
	_, err = fetcher.ListTable(t.Context(), mapping, "team-a", metav1.ListOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("ListTable error = %v, want Forbidden", err)
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("ListTable error = %q, want the server's message %q", err, message)
	}
}

type errAuthorizer string

func (e errAuthorizer) Error() string { return string(e) }
