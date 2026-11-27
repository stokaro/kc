package kubeconfig

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// Kubeconfig represents a kubeconfig file
type Kubeconfig struct {
	Path    string
	Config  *api.Config
	Context string
}

// Context represents a Kubernetes context
type Context struct {
	Name      string
	Cluster   string
	Namespace string
	User      string
	// Kubeconfig is the file that holds the context entry. A change to the
	// entry belongs in this file, as kubectl config writes it there.
	Kubeconfig *Kubeconfig
	// Source is what a client for the context loads. For a context from
	// $KUBECONFIG it is every file of the list, merged the way kubectl
	// merges them, so the cluster and user may come from another file.
	Source *Source
}

// Source is the kubeconfig a client for a context loads.
type Source struct {
	// Path is one file, or the files of $KUBECONFIG joined with the path
	// list separator, in their order. LoadingRules turns it into rules.
	Path string
	// Config is Path loaded; several files are merged as kubectl merges
	// them: each named entry comes whole from the first file that has it.
	Config *api.Config
}

// LoadPath is the Path of the context's Source, or of its file when it
// has no Source.
func (c *Context) LoadPath() string {
	switch {
	case c.Source != nil:
		return c.Source.Path
	case c.Kubeconfig != nil:
		return c.Kubeconfig.Path
	}
	return ""
}

// LoadedConfig is the Config of the context's Source, or of its file when
// it has no Source.
func (c *Context) LoadedConfig() *api.Config {
	switch {
	case c.Source != nil:
		return c.Source.Config
	case c.Kubeconfig != nil:
		return c.Kubeconfig.Config
	}
	return nil
}

// LoadingRules returns the client-go loading rules for a Source path: one
// file is loaded on its own, a list is merged in order the way kubectl
// merges $KUBECONFIG.
func LoadingRules(path string) *clientcmd.ClientConfigLoadingRules {
	files := filepath.SplitList(path)
	if len(files) == 1 {
		return &clientcmd.ClientConfigLoadingRules{ExplicitPath: files[0]}
	}
	return &clientcmd.ClientConfigLoadingRules{Precedence: files}
}

// IsList reports whether a Source path names several files.
func IsList(path string) bool { return len(filepath.SplitList(path)) > 1 }

// Cluster represents a Kubernetes cluster
type Cluster struct {
	Name    string
	Server  string
	Context *Context
}

// Manager handles kubeconfig discovery and management
type Manager struct {
	kubeconfigs []*Kubeconfig
	contexts    []*Context
	clusters    []*Cluster
	// fromEnv is the number of kubeconfigs, at the start of the list, that
	// came from $KUBECONFIG. Their contexts share one merged Source.
	fromEnv int
}

// NewManager creates a new kubeconfig manager
func NewManager() *Manager {
	return &Manager{
		kubeconfigs: make([]*Kubeconfig, 0),
		contexts:    make([]*Context, 0),
		clusters:    make([]*Cluster, 0),
	}
}

// DiscoverKubeconfigs discovers kubeconfigs from KUBECONFIG and ~/.kube
func (m *Manager) DiscoverKubeconfigs() error {
	// DiscoverKubeconfigs takes no context and its callers are in internal/ui.
	log := ctrllog.Log.WithName("kubeconfig.manager") //nolint:forbidigo // no context here, see above
	m.kubeconfigs = make([]*Kubeconfig, 0)
	m.fromEnv = 0

	seen := make(map[string]struct{})
	envVar := strings.TrimSpace(os.Getenv("KUBECONFIG"))

	homeDir, homeErr := os.UserHomeDir()
	log.Info("kubeconfig discovery start", "hasEnv", envVar != "", "homeDirAvailable", homeErr == nil)

	if envVar != "" {
		if err := m.addFromEnv(log, envVar, homeDir, seen); err != nil {
			return err
		}
		m.fromEnv = len(m.kubeconfigs)
		return m.finishDiscovery(log)
	}

	if homeErr != nil {
		return fmt.Errorf("failed to get home directory: %w", homeErr)
	}

	kubeDir := filepath.Join(homeDir, ".kube")
	mainConfigPath := filepath.Join(kubeDir, "config")
	log.Info("discovering kubeconfigs from default directory", "dir", kubeDir)

	if err := m.addKubeconfig(mainConfigPath, seen); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err == nil {
		log.Info("added kubeconfig", "path", mainConfigPath)
	}

	if _, err := os.Stat(kubeDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("kube directory does not exist: %s", kubeDir)
		}
		return fmt.Errorf("failed to stat kube directory: %w", err)
	}

	candidates, err := kubeDirCandidates(kubeDir, mainConfigPath)
	if err != nil {
		return fmt.Errorf("failed to walk kube directory: %w", err)
	}
	m.addCandidates(log, candidates, seen)
	return m.finishDiscovery(log)
}

// addFromEnv adds every kubeconfig of the KUBECONFIG list envVar.
func (m *Manager) addFromEnv(log logr.Logger, envVar, homeDir string, seen map[string]struct{}) error {
	for _, rawPath := range filepath.SplitList(envVar) {
		rawPath = strings.TrimSpace(rawPath)
		if rawPath == "" {
			continue
		}

		normalized, err := normalizeKubeconfigPath(rawPath, homeDir)
		if err != nil {
			return fmt.Errorf("resolving kubeconfig from KUBECONFIG %q: %w", rawPath, err)
		}

		if err := m.addKubeconfig(normalized, seen); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// kubectl skips a file of the list that does not exist.
				log.Info("skipping missing kubeconfig from KUBECONFIG", "path", normalized)
				continue
			}
			return fmt.Errorf("loading kubeconfig from KUBECONFIG %q: %w", normalized, err)
		}
		log.Info("added kubeconfig from env", "path", normalized)
	}
	return nil
}

// kubeDirCandidates lists the files under kubeDir that may be kubeconfigs:
// it skips hidden entries, the cache directory, lock files and the main
// config, which is loaded first.
func kubeDirCandidates(kubeDir, mainConfigPath string) ([]string, error) {
	candidates := make([]string, 0, 64)
	err := filepath.Walk(kubeDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if info.IsDir() {
			if path != kubeDir && (strings.HasPrefix(info.Name(), ".") || info.Name() == "cache") {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasPrefix(info.Name(), ".") || strings.HasSuffix(info.Name(), ".lock") {
			return nil
		}

		if path == mainConfigPath {
			return nil
		}

		candidates = append(candidates, path)
		return nil
	})
	return candidates, err
}

// addCandidates loads the candidate files in parallel and keeps the ones
// that parse as kubeconfigs, in the order of candidates (the walk's lexical
// order). The order decides which file's current-context kc starts in, so
// it must be the same on every run.
func (m *Manager) addCandidates(log logr.Logger, candidates []string, seen map[string]struct{}) {
	paths := make([]string, len(candidates))
	configs := make([]*api.Config, len(candidates))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range max(runtime.NumCPU(), 2) {
		wg.Go(func() {
			for i := range jobs {
				path, err := absKubeconfigPath(candidates[i])
				if err != nil {
					continue
				}
				if config, err := clientcmd.LoadFromFile(path); err == nil {
					paths[i], configs[i] = path, config
				}
			}
		})
	}
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, config := range configs {
		if config == nil {
			continue
		}
		if _, ok := seen[paths[i]]; ok {
			continue
		}
		m.appendKubeconfig(paths[i], config, seen)
		log.V(1).Info("added kubeconfig candidate", "path", paths[i])
	}
}

// finishDiscovery builds the context and cluster lists from the loaded
// kubeconfigs.
func (m *Manager) finishDiscovery(log logr.Logger) error {
	if err := m.buildContextsAndClusters(); err != nil {
		return err
	}
	log.Info("kubeconfig discovery completed", "kubeconfigs", len(m.kubeconfigs), "contexts", len(m.contexts), "clusters", len(m.clusters))
	return nil
}

func normalizeKubeconfigPath(path, homeDir string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("empty kubeconfig path")
	}

	if strings.HasPrefix(trimmed, "~") {
		if homeDir == "" {
			return "", fmt.Errorf("cannot expand ~ without home directory for %q", path)
		}

		switch trimmed {
		case "~":
			trimmed = homeDir
		default:
			sep := string(filepath.Separator)
			if strings.HasPrefix(trimmed, "~"+sep) {
				trimmed = filepath.Join(homeDir, trimmed[2:])
			} else if strings.HasPrefix(trimmed, "~/") {
				trimmed = filepath.Join(homeDir, trimmed[2:])
			}
		}
	}

	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	return absPath, nil
}

func (m *Manager) addKubeconfig(path string, seen map[string]struct{}) error {
	absPath, err := absKubeconfigPath(path)
	if err != nil {
		return err
	}

	if _, ok := seen[absPath]; ok {
		return nil
	}

	config, err := clientcmd.LoadFromFile(absPath)
	if err != nil {
		return err
	}
	m.appendKubeconfig(absPath, config, seen)
	return nil
}

// absKubeconfigPath is the clean absolute form of a kubeconfig path, the
// key of the seen set.
func absKubeconfigPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("kubeconfig path is empty")
	}
	return filepath.Abs(filepath.Clean(path))
}

// appendKubeconfig adds the file loaded from absPath to the list.
func (m *Manager) appendKubeconfig(absPath string, config *api.Config, seen map[string]struct{}) {
	m.kubeconfigs = append(m.kubeconfigs, &Kubeconfig{Path: absPath, Config: config})
	seen[absPath] = struct{}{}
}

// buildContextsAndClusters builds the context and cluster lists from
// kubeconfigs. The files of $KUBECONFIG are one merged kubeconfig, as for
// kubectl: a context name defined in several of them is listed once, from
// the first, and its cluster and user may be in any of them. Every other
// file (kc also reads the files under ~/.kube) stands alone.
//
// The contexts are sorted by name, so every list and pick is the same on
// each run. A name that several standalone files define is listed once per
// file, in the order of the files, so GetContextByName finds the first
// file's entry. The clusters are in file order, sorted by name within each
// file.
func (m *Manager) buildContextsAndClusters() error {
	m.contexts = make([]*Context, 0)
	m.clusters = make([]*Cluster, 0)

	merged, err := m.envSource()
	if err != nil {
		return err
	}
	for i, kubeconfig := range m.kubeconfigs {
		source := &Source{Path: kubeconfig.Path, Config: kubeconfig.Config}
		if i < m.fromEnv {
			source = merged
		}
		// Build contexts
		for _, contextName := range slices.Sorted(maps.Keys(kubeconfig.Config.Contexts)) {
			context := kubeconfig.Config.Contexts[contextName]
			if i < m.fromEnv && merged.Config.Contexts[contextName] != context {
				// An earlier file of $KUBECONFIG defines this name.
				continue
			}
			ctx := &Context{
				Name:       contextName,
				Cluster:    context.Cluster,
				Namespace:  context.Namespace,
				User:       context.AuthInfo,
				Kubeconfig: kubeconfig,
				Source:     source,
			}
			m.contexts = append(m.contexts, ctx)
		}

		// Build clusters
		for _, clusterName := range slices.Sorted(maps.Keys(kubeconfig.Config.Clusters)) {
			clusterObj := &Cluster{
				Name:   clusterName,
				Server: kubeconfig.Config.Clusters[clusterName].Server,
			}
			m.clusters = append(m.clusters, clusterObj)
		}
	}
	// Stable: the entries of one name keep the order of their files.
	slices.SortStableFunc(m.contexts, func(a, b *Context) int { return strings.Compare(a.Name, b.Name) })
	return nil
}

// envSource merges the files of $KUBECONFIG. With one file, it is that
// file as loaded.
func (m *Manager) envSource() (*Source, error) {
	switch m.fromEnv {
	case 0:
		return nil, nil
	case 1:
		kc := m.kubeconfigs[0]
		return &Source{Path: kc.Path, Config: kc.Config}, nil
	}
	paths := make([]string, 0, m.fromEnv)
	for _, kc := range m.kubeconfigs[:m.fromEnv] {
		paths = append(paths, kc.Path)
	}
	path := strings.Join(paths, string(os.PathListSeparator))
	merged, err := LoadingRules(path).Load()
	if err != nil {
		return nil, fmt.Errorf("merging kubeconfigs %s: %w", path, err)
	}
	// The merged contexts are the loaded entries of the files, so that a
	// context can be matched to the file it came from.
	merged.Contexts = make(map[string]*api.Context)
	for _, kc := range m.kubeconfigs[:m.fromEnv] {
		for name, c := range kc.Config.Contexts {
			if _, ok := merged.Contexts[name]; !ok {
				merged.Contexts[name] = c
			}
		}
	}
	return &Source{Path: path, Config: merged}, nil
}

// GetKubeconfigs returns all discovered kubeconfigs
func (m *Manager) GetKubeconfigs() []*Kubeconfig {
	return m.kubeconfigs
}

// GetContexts returns all discovered contexts, sorted by name. A name that
// several standalone files define comes once per file, in file order.
func (m *Manager) GetContexts() []*Context {
	return m.contexts
}

// ContextNames returns the names of the discovered contexts, sorted, each
// once.
func (m *Manager) ContextNames() []string {
	names := make([]string, 0, len(m.contexts))
	for _, c := range m.contexts {
		names = append(names, c.Name)
	}
	return slices.Compact(names)
}

// GetClusters returns all discovered clusters
func (m *Manager) GetClusters() []*Cluster {
	return m.clusters
}

// GetContextByName finds a context by name
func (m *Manager) GetContextByName(name string) *Context {
	for _, ctx := range m.contexts {
		if ctx.Name == name {
			return ctx
		}
	}
	return nil
}

// GetKubeconfigByPath finds a kubeconfig by path
func (m *Manager) GetKubeconfigByPath(path string) *Kubeconfig {
	for _, kc := range m.kubeconfigs {
		if kc.Path == path {
			return kc
		}
	}
	return nil
}

// SetCurrentContext sets the current context for a kubeconfig
func (m *Manager) SetCurrentContext(kubeconfig *Kubeconfig, contextName string) error {
	if kubeconfig.Config.CurrentContext == contextName {
		return nil // Already set
	}

	kubeconfig.Config.CurrentContext = contextName
	kubeconfig.Context = contextName

	// Write back to file
	return clientcmd.WriteToFile(*kubeconfig.Config, kubeconfig.Path)
}

// GetCurrentContext returns the current context for a kubeconfig
func (m *Manager) GetCurrentContext(kubeconfig *Kubeconfig) *Context {
	if kubeconfig.Context != "" {
		return m.GetContextByName(kubeconfig.Context)
	}

	if kubeconfig.Config.CurrentContext != "" {
		return m.GetContextByName(kubeconfig.Config.CurrentContext)
	}

	return nil
}

// GetContextsForKubeconfig returns all contexts for a specific kubeconfig
func (m *Manager) GetContextsForKubeconfig(kubeconfig *Kubeconfig) []*Context {
	var contexts []*Context
	for _, ctx := range m.contexts {
		if ctx.Kubeconfig == kubeconfig {
			contexts = append(contexts, ctx)
		}
	}
	return contexts
}

// GetNamespacesForContext returns the namespaces available for a context
func (m *Manager) GetNamespacesForContext(ctx *Context) ([]string, error) {
	// This would typically involve making an API call to list namespaces
	// For now, return a default list
	return []string{"default", "kube-system", "kube-public", "kube-node-lease"}, nil
}

// CreateClient creates a controller-runtime client for a context
func (m *Manager) CreateClient(ctx *Context) (client.Client, error) {
	// Create client config
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		LoadingRules(ctx.LoadPath()),
		&clientcmd.ConfigOverrides{
			CurrentContext: ctx.Name,
		},
	).ClientConfig()

	if err != nil {
		return nil, fmt.Errorf("failed to create client config: %w", err)
	}

	// Create controller-runtime client
	c, err := client.New(config, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return c, nil
}

// CreateClientForKubeconfig creates a controller-runtime client for a kubeconfig
func (m *Manager) CreateClientForKubeconfig(kubeconfig *Kubeconfig) (client.Client, error) {
	// Create client config
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig.Path},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()

	if err != nil {
		return nil, fmt.Errorf("failed to create client config: %w", err)
	}

	// Create controller-runtime client
	c, err := client.New(config, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return c, nil
}

// CreateClientConfig creates a REST config for a context
func (m *Manager) CreateClientConfig(ctx *Context) (*rest.Config, error) {
	// Create client config
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		LoadingRules(ctx.LoadPath()),
		&clientcmd.ConfigOverrides{
			CurrentContext: ctx.Name,
		},
	).ClientConfig()

	if err != nil {
		return nil, fmt.Errorf("failed to create client config: %w", err)
	}

	return config, nil
}
