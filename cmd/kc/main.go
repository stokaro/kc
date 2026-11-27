package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	pprof "net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-logr/logr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/stokaro/kc/internal/ui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

type cliFlags struct {
	Kubeconfig    string         `help:"Path to kubeconfig file (overrides KUBECONFIG)"`
	Context       string         `help:"Kubeconfig context to open (kubeconfig is not changed)"`
	Namespace     string         `help:"Namespace to open on startup" short:"n"`
	AllNamespaces bool           `help:"Open all namespaces (wins over -n, as in kubectl)" short:"A" name:"all-namespaces"`
	PprofAddr     string         `help:"Start net/http/pprof listener on this address (e.g., localhost:6060)"`
	Verbosity     int            `help:"klog verbosity level (same as --v)" name:"v"`
	As            string         `help:"Username to impersonate for the operation (regular user or service account)"`
	AsGroup       []string       `help:"Group to impersonate for the operation (repeatable)"`
	AsUID         string         `help:"UID to impersonate for the operation"`
	ReadOnly      bool           `name:"readonly" xor:"readonly" help:"Open every context read-only: no edit, delete, namespace creation, debug helper or dangerous commands"`
	Write         bool           `xor:"readonly" help:"Allow writes in every context, ignoring the readOnly config"`
	ShowVersion   versionFlag    `name:"version" help:"Print the kc version and exit; does not read a kubeconfig or contact a cluster"`
	Root          rootCommand    `cmd:"" hidden:"true" default:"1"`
	Get           getCommand     `cmd:"get" help:"Mirror kubectl get"`
	Logs          logsCommand    `cmd:"logs" help:"Mirror kubectl logs"`
	Overview      struct{}       `cmd:"overview" help:"Start in the cluster overview (/overview) of the namespace, or of all namespaces with -A"`
	Dashboard     struct{}       `cmd:"dashboard" help:"Start in the full-screen dashboard of the namespace, or of all namespaces with -A"`
	Version       versionCommand `cmd:"version" help:"Show version information"`
}

type rootCommand struct{}

type getCommand struct {
	Output  string   `help:"Output format (supports yaml)" short:"o"`
	Targets []string `arg:"" optional:"" name:"target" help:"Resource(s) and optional object names"`
}

type logsCommand struct {
	Container string `help:"Container name" short:"c"`
	Follow    bool   `help:"Stream logs (follow)" short:"f"`
	Pod       string `arg:"" help:"Pod name"`
}

type versionCommand struct {
	Client bool `help:"Print only the client version; do not read a kubeconfig or contact a cluster"`
}

// cutoverWriter routes writes to stderr+file until cutover is called, after which
// only the file is used. Shared across zap, klog, and stdlib log so all loggers
// flip at the same time.
type cutoverWriter struct {
	file      io.Writer
	stderr    io.Writer
	useStderr atomic.Bool
}

func newCutoverWriter(file, stderr io.Writer) *cutoverWriter {
	w := &cutoverWriter{file: file, stderr: stderr}
	w.useStderr.Store(true)
	return w
}

func (w *cutoverWriter) Write(p []byte) (int, error) {
	if w == nil || w.file == nil {
		return len(p), nil
	}
	if w.useStderr.Load() && w.stderr != nil {
		return io.MultiWriter(w.stderr, w.file).Write(p)
	}
	return w.file.Write(p)
}

func (w *cutoverWriter) Sync() error {
	if w == nil {
		return nil
	}
	// Best-effort sync; ignore errors to avoid noisy logs during shutdown.
	if ws, ok := w.file.(zapcore.WriteSyncer); ok {
		_ = ws.Sync()
	}
	if w.useStderr.Load() {
		if ws, ok := w.stderr.(zapcore.WriteSyncer); ok {
			_ = ws.Sync()
		}
	}
	return nil
}

func main() {
	var cli cliFlags
	ctx := kong.Parse(&cli, cliOptions()...)

	if kcPath := strings.TrimSpace(cli.Kubeconfig); kcPath != "" {
		_ = os.Setenv("KUBECONFIG", kcPath)
	}

	// Configure logging. Default: silent. With -v>=1: log to stderr during startup,
	// then to ~/.kc/debug.log only once the UI starts. With -v>=3: enable debug logs.
	switchToUILogger, debugLogPath := setupControllerRuntimeLogger(cli.Verbosity)
	// client-go prints API deprecation warnings ("v1 Endpoints is deprecated")
	// to stderr by default, which draws over the TUI. Send them through klog,
	// which goes to the debug log or nowhere, like the other logs.
	rest.SetDefaultWarningHandler(rest.WarningLogger{})
	startPprofServer(strings.TrimSpace(cli.PprofAddr))

	if commandMatches(ctx.Command(), "kc version", "version") {
		if err := runVersionCommand(context.Background(), os.Stdout, os.Stderr, cli.Version.Client); err != nil {
			os.Exit(1)
		}
		return
	}

	intent, nsOverride, err := deriveStartupIntent(ctx.Command(), &cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ns := strings.TrimSpace(nsOverride)
	if ns == "" {
		ns = strings.TrimSpace(cli.Namespace)
	}

	// Run the application
	runCfg := newRunConfig(&cli, intent, ns)
	runCfg.DebugLogPath = debugLogPath
	runCfg.SwitchToFileLogger = switchToUILogger
	runCfg.ReadOnly = readOnlyOverride(&cli)
	// ui.Run resets the terminal after the UI exits; an error that stops
	// kc before the UI starts comes back with the terminal untouched.
	if err := ui.Run(context.Background(), runCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// cliOptions returns the kong options main parses the command line with.
func cliOptions() []kong.Option {
	return []kong.Option{
		kong.Name("kc"),
		kong.Description("Kubernetes Commander (kc) - A TUI for Kubernetes."),
		kong.UsageOnError(),
	}
}

// readOnlyOverride maps --readonly and --write to the UI's read-only
// override. Kong refuses both at once (xor), so at most one is set.
func readOnlyOverride(cli *cliFlags) ui.ReadOnlyOverride {
	switch {
	case cli.ReadOnly:
		return ui.ReadOnlyForced
	case cli.Write:
		return ui.ReadOnlyWriteForced
	default:
		return ui.ReadOnlyFromConfig
	}
}

func deriveStartupIntent(cmd string, cli *cliFlags) (ui.StartupIntent, string, error) {
	var intent ui.StartupIntent
	if cli == nil {
		return intent, "", nil
	}

	switch {
	case commandMatches(cmd, "", "kc", "kc root", "root"):
		if cli.AllNamespaces {
			return intent, "", nil
		}
		return intent, strings.TrimSpace(cli.Namespace), nil
	case commandMatches(cmd, "kc get", "get"):
		if len(cli.Get.Targets) == 0 {
			return intent, "", errors.New("kc get requires at least one target")
		}
		getIntent, err := parseGetIntent(cli.Get.Targets, cli.Get.Output)
		if err != nil {
			return intent, "", err
		}
		ns := selectNamespace(cli.Namespace, "")
		if cli.AllNamespaces {
			if err := checkAllNamespacesGet(getIntent); err != nil {
				return intent, "", err
			}
			ns = ""
		}
		intent.Verb = ui.KubectlVerbGet
		intent.Namespace = ns
		intent.Get = getIntent
		return intent, ns, nil
	case commandMatches(cmd, "kc logs", "logs"):
		if strings.TrimSpace(cli.Logs.Pod) == "" {
			return intent, "", errors.New("kc logs requires a pod name")
		}
		if cli.AllNamespaces {
			return intent, "", errAllNamespacesLogs
		}
		logsIntent := &ui.LogsIntent{
			Pod:       strings.TrimSpace(cli.Logs.Pod),
			Container: strings.TrimSpace(cli.Logs.Container),
			Follow:    cli.Logs.Follow,
		}
		ns := selectNamespace(cli.Namespace, "")
		intent.Verb = ui.KubectlVerbLogs
		intent.Namespace = ns
		intent.Logs = logsIntent
		return intent, ns, nil
	case commandMatches(cmd, "kc overview", "overview"), commandMatches(cmd, "kc dashboard", "dashboard"):
		ns := selectNamespace(cli.Namespace, "")
		if cli.AllNamespaces {
			ns = ""
		}
		intent.Verb = ui.KubectlVerbOverview
		if commandMatches(cmd, "kc dashboard", "dashboard") {
			intent.Verb = ui.KubectlVerbDashboard
		}
		intent.Namespace = ns
		return intent, ns, nil
	default:
		return intent, "", fmt.Errorf("unsupported command %q", cmd)
	}
}

func commandMatches(cmd string, options ...string) bool {
	clean := strings.TrimSpace(cmd)
	for _, opt := range options {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			if clean == "" {
				return true
			}
			continue
		}
		if clean == opt || strings.HasPrefix(clean, opt+" ") {
			return true
		}
		if !strings.HasPrefix(opt, "kc ") && (clean == opt || strings.HasPrefix(clean, opt+" ")) {
			return true
		}
	}
	return false
}

func selectNamespace(global, override string) string {
	if ns := strings.TrimSpace(override); ns != "" {
		return ns
	}
	return strings.TrimSpace(global)
}

// setupControllerRuntimeLogger configures controller-runtime and klog logging.
// Default (verbosity 0): discard all logs. With verbosity >=1 logs are written to stderr
// during startup and ~/.kc/debug.log; once the returned switch function is invoked, logs
// are written to the file only. The zap logger level is derived from the provided
// verbosity so klog -v and controller-runtime logr stay aligned.
func setupControllerRuntimeLogger(verbosity int) (func(), string) {
	// Register klog flags so we can set -v programmatically even though Kong owns CLI parsing.
	klog.InitFlags(nil)

	if verbosity < 1 {
		ctrllog.SetLogger(logr.Discard())
		klog.SetLogger(logr.Discard())
		klog.SetOutput(io.Discard)
		log.SetOutput(io.Discard)
		return nil, ""
	}

	zapLevel := zapLevelForVerbosity(verbosity)
	_ = flag.Set("v", strconv.Itoa(verbosity))
	_ = flag.Set("logtostderr", "false")
	_ = flag.Set("alsologtostderr", "false")

	home, err := os.UserHomeDir()
	if err != nil {
		ctrllog.SetLogger(logr.Discard())
		klog.SetLogger(logr.Discard())
		klog.SetOutput(io.Discard)
		log.SetOutput(io.Discard)
		return nil, ""
	}

	dir := filepath.Join(home, ".kc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		ctrllog.SetLogger(logr.Discard())
		klog.SetLogger(logr.Discard())
		klog.SetOutput(io.Discard)
		log.SetOutput(io.Discard)
		return nil, ""
	}

	fpath := filepath.Join(dir, "debug.log")
	_ = os.Remove(fpath)
	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		ctrllog.SetLogger(logr.Discard())
		klog.SetLogger(logr.Discard())
		klog.SetOutput(io.Discard)
		log.SetOutput(io.Discard)
		return nil, ""
	}

	writer := newCutoverWriter(f, os.Stderr)
	startupLogger := crzap.New(
		crzap.UseDevMode(true),
		crzap.WriteTo(writer),
		crzap.Level(zapLevel),
		crzap.Encoder(newLevelAwareEncoder()),
		crzap.RawZapOpts(zap.AddCaller()),
	)
	ctrllog.SetLogger(startupLogger)
	log.SetOutput(writer)
	log.SetFlags(0)
	klog.SetOutput(writer)
	klog.SetLogger(ctrllog.Log)

	return func() {
		fileLogger := crzap.New(
			crzap.UseDevMode(true),
			crzap.WriteTo(writer),
			crzap.Level(zapLevel),
			crzap.Encoder(newLevelAwareEncoder()),
			crzap.RawZapOpts(zap.AddCaller()),
		)
		writer.useStderr.Store(false)
		ctrllog.SetLogger(fileLogger)
		log.SetOutput(writer)
		log.SetFlags(0)
		klog.SetOutput(writer)
		klog.SetLogger(ctrllog.Log)
	}, fpath
}

func zapLevelForVerbosity(verbosity int) zapcore.Level {
	if verbosity > 127 {
		verbosity = 127
	}
	if verbosity < 0 {
		return zapcore.InfoLevel
	}
	return zapcore.Level(-verbosity)
}

func newLevelAwareEncoder() zapcore.Encoder {
	ecfg := zap.NewDevelopmentEncoderConfig()
	ecfg.EncodeLevel = func(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
		if level < zapcore.DebugLevel {
			enc.AppendString(fmt.Sprintf("V%d", -int(level)))
			return
		}
		zapcore.CapitalLevelEncoder(level, enc)
	}
	ecfg.EncodeTime = zapcore.RFC3339TimeEncoder
	return zapcore.NewConsoleEncoder(ecfg)
}

func startPprofServer(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		fmt.Fprintf(os.Stderr, "pprof: listening on http://%s/debug/pprof/\n", addr)
		// No read or write timeout: a CPU profile or trace streams for as long
		// as the client asks.
		srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "pprof server error: %v\n", err)
		}
	}()
}
