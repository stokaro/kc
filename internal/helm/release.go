// Package helm reads Helm 3 release records as Helm's storage drivers write
// them. It decodes only the fields kc shows and does not depend on Helm.
//
// Helm's Secret driver stores one Secret per revision, of type
// helm.sh/release.v1, named sh.helm.release.v1.<release>.v<revision>. The
// "release" key holds the release as JSON, gzipped and base64 encoded (on
// top of the base64 encoding of Secret data in the Kubernetes API).
package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Storage labels and keys used by Helm's Secret and ConfigMap drivers.
const (
	SecretType   = "helm.sh/release.v1" // #nosec G101 -- a Secret type name, not a credential
	ReleaseKey   = "release"
	OwnerLabel   = "owner"
	OwnerValue   = "helm"
	NameLabel    = "name"
	StatusLabel  = "status"
	VersionLabel = "version"
)

// maxReleaseSize bounds the decompressed size of one release record, so a
// corrupt or hostile record cannot exhaust memory. Helm's own limit for the
// compressed record is the 1 MiB object size limit of etcd.
const maxReleaseSize = 64 << 20

var gzipMagic = []byte{0x1f, 0x8b, 0x08}

// Release is the part of a Helm release record that kc shows.
type Release struct {
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Version   int             `json:"version"`
	Info      Info            `json:"info"`
	Chart     *Chart          `json:"chart,omitempty"`
	Config    json.RawMessage `json:"config,omitempty"`
	Manifest  string          `json:"manifest"`
	Hooks     []Hook          `json:"hooks,omitempty"`
}

// Info is the status block of a release.
type Info struct {
	FirstDeployed Time   `json:"first_deployed"`
	LastDeployed  Time   `json:"last_deployed"`
	Deleted       Time   `json:"deleted"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// Chart holds the chart metadata. Templates and files are not decoded.
type Chart struct {
	Metadata *ChartMetadata `json:"metadata,omitempty"`
}

// ChartMetadata is the Chart.yaml content kc shows.
type ChartMetadata struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	AppVersion string `json:"appVersion,omitempty"`
}

// Hook is a rendered chart hook.
type Hook struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	Manifest string   `json:"manifest"`
	Events   []string `json:"events,omitempty"`
}

// Time is Helm's JSON time: RFC 3339, with "" (or null) for the zero time.
type Time struct{ time.Time }

// MarshalJSON writes "" for the zero time, as Helm does.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte(`""`), nil
	}
	return t.Time.MarshalJSON()
}

// UnmarshalJSON accepts "", null and RFC 3339 times.
func (t *Time) UnmarshalJSON(b []byte) error {
	s := string(bytes.TrimSpace(b))
	if s == `""` || s == "null" {
		t.Time = time.Time{}
		return nil
	}
	return t.Time.UnmarshalJSON(b)
}

// ChartName returns the chart as Helm lists it: name-version.
func (r *Release) ChartName() string {
	if r.Chart == nil || r.Chart.Metadata == nil {
		return ""
	}
	m := r.Chart.Metadata
	if m.Version == "" {
		return m.Name
	}
	return m.Name + "-" + m.Version
}

// AppVersion returns the chart's appVersion, or "".
func (r *Release) AppVersion() string {
	if r.Chart == nil || r.Chart.Metadata == nil {
		return ""
	}
	return r.Chart.Metadata.AppVersion
}

// Decode decodes a release record as Helm's drivers store it: base64, then
// gzip (when the gzip header is present), then JSON. For a Secret, pass
// the value after the Kubernetes API's own base64 layer is removed; for a
// ConfigMap, pass the data value as is.
func Decode(encoded []byte) (*Release, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("release record is not base64: %w", err)
	}
	if bytes.HasPrefix(raw, gzipMagic) {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("release record is not gzip: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(zr, maxReleaseSize+1))
		_ = zr.Close()
		if err != nil {
			return nil, fmt.Errorf("release record is not gzip: %w", err)
		}
		if len(data) > maxReleaseSize {
			return nil, fmt.Errorf("release record is larger than %d MiB", maxReleaseSize>>20)
		}
		raw = data
	}
	var rel Release
	if err := json.Unmarshal(raw, &rel); err != nil {
		return nil, fmt.Errorf("release record is not JSON: %w", err)
	}
	if rel.Name == "" {
		return nil, errors.New("release record has no name")
	}
	return &rel, nil
}

// Encode encodes a release the way Helm's drivers do. kc never writes
// releases; tests use it to build fixtures.
func Encode(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := zw.Write(data); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
