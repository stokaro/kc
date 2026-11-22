package portforward

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientportforward "k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/streaming/pkg/httpstream"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ClientRunner forwards through the API server with client-go, as
// kubectl port-forward does: SPDY tunneled over a websocket first, plain
// SPDY when the server or a proxy refuses the websocket upgrade.
type ClientRunner struct {
	Config *rest.Config
}

var _ Runner = ClientRunner{}

// Run implements Runner.
func (c ClientRunner) Run(ctx context.Context, spec Spec, ready func(int32)) error {
	log := ctrllog.FromContext(ctx).WithName("portforward")
	cs, err := kubernetes.NewForConfig(c.Config)
	if err != nil {
		return fmt.Errorf("building client: %w", err)
	}
	u := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(spec.Namespace).
		Name(spec.Pod).
		SubResource("portforward").
		URL()
	dialer, err := newDialer(log, c.Config, u)
	if err != nil {
		return err
	}
	address := spec.Address
	if address == "" {
		address = DefaultAddress
	}
	ports := []string{fmt.Sprintf("%d:%d", spec.LocalPort, spec.RemotePort)}
	readyCh := make(chan struct{})
	errOut := logWriter(log)
	defer errOut.Close()
	pf, err := clientportforward.NewOnAddressesForStreamingWithContext(ctx, dialer, []string{address}, ports, readyCh, io.Discard, errOut)
	if err != nil {
		return err
	}
	go func() {
		select {
		case <-readyCh:
		case <-ctx.Done():
			return
		}
		bound, err := pf.GetPorts()
		if err != nil || len(bound) == 0 {
			return
		}
		ready(int32(bound[0].Local))
	}()
	log.Info("client portforward", "gvr", "v1/pods", "subresource", "portforward", "namespace", spec.Namespace, "name", spec.Pod, "address", address, "ports", ports[0], "context", spec.Context)
	return pf.ForwardPorts()
}

func newDialer(log logr.Logger, cfg *rest.Config, u *url.URL) (httpstream.Dialer, error) {
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("building SPDY transport: %w", err)
	}
	spdyDialer := spdy.NewDialerForStreaming(upgrader, &http.Client{Transport: transport}, http.MethodPost, u)
	ws, err := clientportforward.NewSPDYOverWebsocketDialerForStreamingWithLogger(log, u, cfg)
	if err != nil {
		return nil, fmt.Errorf("building websocket dialer: %w", err)
	}
	return clientportforward.NewFallbackDialerForStreaming(ws, spdyDialer, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	}), nil
}

// logWriter turns client-go's per-connection error lines ("an error
// occurred forwarding ...") into log lines instead of terminal output.
func logWriter(log logr.Logger) io.WriteCloser {
	r, w := io.Pipe()
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				log.Info("port forward message", "text", line)
			}
		}
		_ = r.Close()
	}()
	return w
}
