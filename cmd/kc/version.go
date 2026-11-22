package main

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/alecthomas/kong"
	apimachineryversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc"
)

type serverVersionGetter interface {
	ServerVersion() (*apimachineryversion.Info, error)
}

type versionInfo struct {
	kcVersion     string
	commit        string
	date          string
	clientGo      string
	serverVersion *apimachineryversion.Info
}

// versionFlag implements `kc --version`. Kong runs its hook while it parses
// the command line, so kc prints the client version and exits before it reads
// a kubeconfig, sets up logging or opens a connection. Package managers run
// it as their install check on machines without cluster access.
type versionFlag bool

// BeforeReset is the kong hook that runs before flag values are applied.
func (versionFlag) BeforeReset(app *kong.Kong) error {
	printVersionInfo(app.Stdout, clientVersionInfo())
	app.Exit(0)
	return nil
}

// runVersionCommand implements `kc version`. With clientOnly it prints the
// client version and never loads a kubeconfig. Otherwise it also asks the
// cluster for its version and fails, like `kubectl version`, when it cannot.
func runVersionCommand(ctx context.Context, stdout, stderr io.Writer, clientOnly bool) error {
	if clientOnly {
		printVersionInfo(stdout, clientVersionInfo())
		return nil
	}
	discoveryClient, err := buildDiscoveryClient(ctx)
	info, serverErr := collectVersionInfo(ctx, discoveryClient)
	if err != nil {
		serverErr = err
	}
	printVersionInfo(stdout, info)
	if serverErr != nil {
		fmt.Fprintf(stderr, "Error fetching server version: %v\n", serverErr)
		fmt.Fprintln(stderr, "Use 'kc version --client' or 'kc --version' to print only the client version.")
		return serverErr
	}
	return nil
}

func buildDiscoveryClient(ctx context.Context) (serverVersionGetter, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("creating client config: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating discovery client: %w", err)
	}

	return &loggingDiscoveryClient{
		ctx:      ctx,
		host:     restConfig.Host,
		delegate: discoveryClient,
	}, nil
}

// clientVersionInfo returns what kc knows about itself, without any I/O.
func clientVersionInfo() versionInfo {
	bi, _ := debug.ReadBuildInfo()
	v, c, d := resolveBuildVersion(version, commit, date, bi)
	return versionInfo{
		kcVersion: v,
		commit:    c,
		date:      d,
		clientGo:  kc.ClientGoVersion(),
	}
}

// resolveBuildVersion fills in the values a release build sets through
// -ldflags when the binary was built without them. `go install ...@vX.Y.Z`
// records the module version in the build info, and a build from a git
// checkout records the commit and its time. Values from -ldflags win.
func resolveBuildVersion(v, c, d string, bi *debug.BuildInfo) (string, string, string) {
	if bi == nil {
		return v, c, d
	}
	if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch {
		case s.Key == "vcs.revision" && c == "unknown" && s.Value != "":
			c = s.Value
		case s.Key == "vcs.time" && d == "unknown" && s.Value != "":
			d = s.Value
		}
	}
	return v, c, d
}

func collectVersionInfo(ctx context.Context, discoveryClient serverVersionGetter) (versionInfo, error) {
	info := clientVersionInfo()

	if discoveryClient == nil {
		return info, nil
	}

	serverVersion, err := discoveryClient.ServerVersion()
	if err != nil {
		return info, err
	}

	info.serverVersion = serverVersion
	return info, nil
}

func printVersionInfo(out io.Writer, info versionInfo) {
	fmt.Fprintf(out, "kc Version: %s\n", info.kcVersion)
	fmt.Fprintf(out, "Client Version (client-go): %s\n", info.clientGo)
	if info.serverVersion != nil {
		fmt.Fprintf(out, "Server Version: %s\n", info.serverVersion.GitVersion)
	}
	fmt.Fprintf(out, "Commit: %s\n", info.commit)
	fmt.Fprintf(out, "Date: %s\n", info.date)
}

type loggingDiscoveryClient struct {
	ctx      context.Context
	host     string
	delegate discovery.ServerVersionInterface
}

func (l *loggingDiscoveryClient) ServerVersion() (*apimachineryversion.Info, error) {
	log := ctrllog.FromContext(l.ctx).WithName("version")
	log.Info("requesting server version", "host", l.host)
	return l.delegate.ServerVersion()
}
