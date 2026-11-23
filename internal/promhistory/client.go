package promhistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/pkg/appconfig"
)

// maxResponseBytes bounds one response body. A history is a few aggregated
// series of about a hundred points; a larger body is not one of ours.
const maxResponseBytes = 4 << 20

// maxSeries bounds the series one query may return. Every query sums or
// counts to one series; more means the server ignored the aggregation.
const maxSeries = 8

// Client asks one Prometheus-compatible server, behind a Kubernetes
// Service, through the API server's service proxy.
type Client struct {
	http *http.Client
	base string // up to and including the path prefix, no trailing slash
	// Source names the backend for the user: "monitoring/prometheus-k8s:9090".
	Source string
	labels map[string]string
}

// NewServiceProxyClient returns a client for backend b that sends its
// requests with the credentials, impersonation and TLS settings of cfg.
func NewServiceProxyClient(cfg *rest.Config, b appconfig.MetricsBackendConfig) (*Client, error) {
	if b.Namespace == "" || b.Service == "" {
		return nil, errors.New("metricsHistory backend needs namespace and service")
	}
	switch b.Scheme {
	case "", "http", "https":
	default:
		return nil, fmt.Errorf("metricsHistory backend scheme %q: want http or https", b.Scheme)
	}
	if _, err := newSelector(b.Labels); err != nil {
		return nil, fmt.Errorf("metricsHistory backend: %w", err)
	}
	hc, err := rest.HTTPClientFor(rest.CopyConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("metricsHistory client: %w", err)
	}
	svc := b.Service
	switch {
	case b.Scheme != "":
		svc = b.Scheme + ":" + b.Service + ":" + b.Port
	case b.Port != "":
		svc = b.Service + ":" + b.Port
	}
	// Host may carry a path (a Rancher cluster proxy); APIPath is not
	// used, the core API path is spelled out.
	base := strings.TrimSuffix(cfg.Host, "/") + "/api/v1/namespaces/" + url.PathEscape(b.Namespace) +
		"/services/" + url.PathEscape(svc) + "/proxy"
	if p := strings.Trim(b.PathPrefix, "/"); p != "" {
		base += "/" + p
	}
	source := b.Namespace + "/" + b.Service
	if b.Port != "" {
		source += ":" + b.Port
	}
	return &Client{http: hc, base: base, Source: source, labels: b.Labels}, nil
}

// sample is one point of a series.
type sample struct {
	at    time.Time
	value float64
}

type series struct {
	metric map[string]string
	points []sample
}

type apiResponse struct {
	Status    string   `json:"status"`
	ErrorType string   `json:"errorType"`
	Error     string   `json:"error"`
	Warnings  []string `json:"warnings"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// k8sStatus is the error body of the API server itself (forbidden on
// services/proxy, service not found, no endpoints).
type k8sStatus struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// get sends one query to path with params and decodes the result series.
func (c *Client) get(ctx context.Context, path string, params url.Values) ([]series, []string, error) {
	ctrllog.FromContext(ctx).WithName("promhistory").Info("client get", "backend", c.Source, "path", path, "query", params.Get("query"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// The *url.Error repeats the URL; the cause is enough, and the
		// message then cannot carry anything from the host string.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("prometheus %s: %w", c.Source, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("prometheus %s: reading response: %w", c.Source, err)
	}
	if len(body) > maxResponseBytes {
		return nil, nil, fmt.Errorf("prometheus %s: response larger than %d bytes", c.Source, maxResponseBytes)
	}
	var r apiResponse
	jsonErr := json.Unmarshal(body, &r)
	if resp.StatusCode != http.StatusOK || r.Status != "success" {
		return nil, nil, c.statusError(resp.StatusCode, body, r, jsonErr)
	}
	if jsonErr != nil {
		return nil, nil, fmt.Errorf("prometheus %s: decoding response: %w", c.Source, jsonErr)
	}
	out, err := decodeResult(r.Data.ResultType, r.Data.Result)
	if err != nil {
		return nil, nil, fmt.Errorf("prometheus %s: %w", c.Source, err)
	}
	if len(out) > maxSeries {
		return nil, nil, fmt.Errorf("prometheus %s: %d series where one was expected", c.Source, len(out))
	}
	return out, r.Warnings, nil
}

// statusError explains a failed request: Prometheus' own error, the API
// server's Status, or the HTTP status alone.
func (c *Client) statusError(code int, body []byte, r apiResponse, jsonErr error) error {
	switch {
	case jsonErr == nil && r.Error != "":
		return fmt.Errorf("prometheus %s: %s: %s", c.Source, r.ErrorType, r.Error)
	case code == http.StatusForbidden || code == http.StatusUnauthorized:
		return forbiddenError{msg: fmt.Sprintf("prometheus %s: %s (needs get on services/proxy)", c.Source, k8sMessage(body, http.StatusText(code)))}
	case code != http.StatusOK:
		return fmt.Errorf("prometheus %s: HTTP %d: %s", c.Source, code, k8sMessage(body, http.StatusText(code)))
	default:
		return fmt.Errorf("prometheus %s: unexpected response status %q", c.Source, r.Status)
	}
}

// k8sMessage returns the message of an API server Status body, or def.
func k8sMessage(body []byte, def string) string {
	var st k8sStatus
	if json.Unmarshal(body, &st) == nil && st.Kind == "Status" && st.Message != "" {
		return st.Message
	}
	return def
}

func decodeResult(resultType string, raw json.RawMessage) ([]series, error) {
	switch resultType {
	case "matrix":
		var m []struct {
			Metric map[string]string    `json:"metric"`
			Values [][2]json.RawMessage `json:"values"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("decoding matrix: %w", err)
		}
		out := make([]series, 0, len(m))
		for _, s := range m {
			pts := make([]sample, 0, len(s.Values))
			for _, v := range s.Values {
				p, err := decodeSample(v)
				if err != nil {
					return nil, err
				}
				pts = append(pts, p)
			}
			out = append(out, series{metric: s.Metric, points: pts})
		}
		return out, nil
	case "vector":
		var v []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("decoding vector: %w", err)
		}
		out := make([]series, 0, len(v))
		for _, s := range v {
			p, err := decodeSample(s.Value)
			if err != nil {
				return nil, err
			}
			out = append(out, series{metric: s.Metric, points: []sample{p}})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected result type %q", resultType)
	}
}

// decodeSample reads [<unix seconds>, "<value>"]. NaN stays NaN; the
// caller treats it as a missing point.
func decodeSample(v [2]json.RawMessage) (sample, error) {
	var ts float64
	if err := json.Unmarshal(v[0], &ts); err != nil {
		return sample{}, fmt.Errorf("decoding sample time: %w", err)
	}
	var s string
	if err := json.Unmarshal(v[1], &s); err != nil {
		return sample{}, fmt.Errorf("decoding sample value: %w", err)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return sample{}, fmt.Errorf("decoding sample value %q: %w", s, err)
	}
	sec, frac := math.Modf(ts)
	return sample{at: time.Unix(int64(sec), int64(math.Round(frac*1e3))*int64(time.Millisecond)), value: f}, nil
}

func formatTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixMilli())/1e3, 'f', -1, 64)
}

func (c *Client) queryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]series, []string, error) {
	return c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {q},
		"start": {formatTime(start)},
		"end":   {formatTime(end)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	})
}

func (c *Client) query(ctx context.Context, q string, at time.Time) ([]series, []string, error) {
	return c.get(ctx, "/api/v1/query", url.Values{"query": {q}, "time": {formatTime(at)}})
}
