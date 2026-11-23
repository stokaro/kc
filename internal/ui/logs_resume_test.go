package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/logresume"
	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/models"
)

// scriptedBody returns its text, then fails or blocks until canceled.
type scriptedBody struct {
	r    io.Reader
	fail bool
	ctx  context.Context
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 || !errors.Is(err, io.EOF) {
		return n, err
	}
	if b.fail {
		return 0, io.ErrUnexpectedEOF
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *scriptedBody) Close() error { return nil }

// TestLogsViewerKeepsItsStateAcrossAReconnect drives a viewer on a resumed
// stream whose connection fails once. The filter, the kept records and the
// view stay; the reconnect shows as notices and in the footer.
func TestLogsViewerKeepsItsStateAcrossAReconnect(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		bodies := []string{
			"2026-11-22T10:00:01.1Z a\n2026-11-22T10:00:01.2Z healthz\n",
			"2026-11-22T10:00:01.1Z a\n2026-11-22T10:00:01.2Z healthz\n2026-11-22T10:00:03Z c\n",
		}
		opens := 0
		openBody := func(ctx context.Context, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			opens++
			if opens > len(bodies) {
				return nil, errors.New("unexpected open")
			}
			return &scriptedBody{r: strings.NewReader(bodies[opens-1]), fail: opens == 1, ctx: ctx}, nil
		}
		check := func(context.Context) (logresume.Instance, bool) {
			return logresume.Instance{PodUID: "u", Running: true}, true
		}
		open := func(spec models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
			ctx, cancel := context.WithCancel(t.Context())
			r, err := logresume.Open(ctx, logresume.Config{Options: *spec.PodLogOptions(), Open: openBody, Check: check})
			if err != nil {
				cancel()
				return nil, nil, err
			}
			return r, cancel, nil
		}
		v := NewLogsViewer("logs:ns/p/c", models.LogsSpec{Namespace: "ns", Pod: "p", Container: "c", Follow: true}, open, "")
		v.SetDimensions(80, 20)
		v.inner.Resize(80, 18)
		defer v.Close()

		cmd := v.Init()
		step := func() {
			t.Helper()
			if cmd == nil {
				t.Fatal("the stream stopped reading")
			}
			_, cmd = v.Update(cmd())
		}
		step() // opened
		step() // a
		v.SetQuery(logview.MustParseQuery("!healthz"))
		step() // healthz, hidden
		step() // [kc] reconnecting
		synctest.Wait()
		if !strings.Contains(v.statusText(), "(reconnecting)") {
			t.Fatalf("statusText() = %q while waiting to reconnect", v.statusText())
		}
		step() // [kc] reconnected
		step() // c
		want := []string{"a", "[kc] reconnecting in 1s: unexpected EOF", "[kc] reconnected", "c"}
		if got := plainLines(v); !equalLines(got, want) {
			t.Fatalf("display = %q, want %q", got, want)
		}
		if v.log.Len() != 5 || v.Query() != "!healthz" {
			t.Fatalf("Len %d, query %q: the reconnect must keep the records and the filter", v.log.Len(), v.Query())
		}
		if strings.Contains(v.statusText(), "reconnecting") {
			t.Fatalf("statusText() = %q after the reconnect", v.statusText())
		}
	})
}
