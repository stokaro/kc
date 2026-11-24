package execctx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Mode selects how a generated kubeconfig relates to the user's files.
type Mode string

const (
	// ModeOverlay writes a small file that goes first in KUBECONFIG, before
	// the user's files. It sets the current context and the namespace and
	// copies only entries it must change or that an earlier file shadows.
	ModeOverlay Mode = "overlay"
	// ModeCopy writes a full copy of what kc loaded for the context: the
	// file that defines it, or the merged files of $KUBECONFIG.
	ModeCopy Mode = "copy"
)

// Source is a user's kubeconfig file that follows the overlay in
// KUBECONFIG. Sources keep the order kubectl merges them in.
type Source struct {
	Path   string
	Config *clientcmdapi.Config
}

// Files writes generated kubeconfigs into a private directory that kc owns.
// Each consumer writes to its own slot. A slot keeps its path, and in
// overlay mode its KUBECONFIG value, for the life of Files, so a running
// shell follows rewrites of its slot. The user's kubeconfig files are only
// read.
//
// Files is not safe for concurrent use; kc calls it from the UI goroutine.
type Files struct {
	dir     string
	mode    Mode
	sources []Source
}

var slotName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// NewFiles creates the directory. It lives under a per-user root in the
// temp directory; directories left by kc processes that are gone are
// removed. Close removes the directory.
func NewFiles(mode Mode, sources []Source) (*Files, error) {
	if mode != ModeCopy {
		mode = ModeOverlay
	}
	dir, err := makePrivateDir()
	if err != nil {
		return nil, err
	}
	return &Files{dir: dir, mode: mode, sources: append([]Source(nil), sources...)}, nil
}

// Dir returns the private directory.
func (f *Files) Dir() string { return f.dir }

// Path returns the file of slot.
func (f *Files) Path(slot string) string {
	return filepath.Join(f.dir, slot+".kubeconfig")
}

// EnvValue returns the KUBECONFIG value for slot. It does not depend on
// the context written to the slot.
func (f *Files) EnvValue(slot string) string {
	if f.mode == ModeCopy {
		return f.Path(slot)
	}
	parts := []string{f.Path(slot)}
	for _, s := range f.sources {
		if s.Path != "" {
			parts = append(parts, s.Path)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

// Write renders c into slot and returns the KUBECONFIG value to use. The
// file is replaced atomically with mode 0600, so a process that reads it
// sees the old or the new content.
func (f *Files) Write(slot string, c Context) (string, error) {
	if !slotName.MatchString(slot) {
		return "", fmt.Errorf("invalid kubeconfig slot %q", slot)
	}
	var (
		cfg *clientcmdapi.Config
		err error
	)
	if f.mode == ModeCopy {
		cfg, err = RenderCopy(c)
	} else {
		cfg, err = RenderOverlay(c, f.sources)
	}
	if err != nil {
		return "", err
	}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return "", fmt.Errorf("encoding kubeconfig: %w", err)
	}
	if err := writeFileAtomic(f.Path(slot), data); err != nil {
		return "", err
	}
	return f.EnvValue(slot), nil
}

// Remove deletes the file of slot.
func (f *Files) Remove(slot string) error {
	if !slotName.MatchString(slot) {
		return nil
	}
	err := os.Remove(f.Path(slot))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Close removes the directory and every slot in it.
func (f *Files) Close() error { return os.RemoveAll(f.dir) }

// RenderCopy returns c.Config (the file that defines c, or the merged
// files of $KUBECONFIG), with c's context current,
// its namespace set and impersonation applied to its user. Relative file
// references are made absolute, because the copy lives elsewhere.
func RenderCopy(c Context) (*clientcmdapi.Config, error) {
	if c.Config == nil {
		return nil, fmt.Errorf("kubeconfig for context %q is not loaded", c.Context)
	}
	cfg := c.Config.DeepCopy()
	if err := clientcmd.ResolveLocalPaths(cfg); err != nil {
		return nil, fmt.Errorf("resolving kubeconfig paths: %w", err)
	}
	kctx := cfg.Contexts[c.Context]
	if kctx == nil {
		return nil, fmt.Errorf("context %q not found in %s", c.Context, displayPath(c.Kubeconfig))
	}
	cfg.CurrentContext = c.Context
	if ns := strings.TrimSpace(c.Namespace); ns != "" {
		kctx.Namespace = ns
	}
	if !c.Impersonation.IsZero() {
		auth := cfg.AuthInfos[kctx.AuthInfo]
		if auth == nil {
			return nil, fmt.Errorf("context %q has no user to impersonate with", c.Context)
		}
		applyImpersonation(auth, c.Impersonation)
	}
	return cfg, nil
}

// RenderOverlay returns the file that goes before sources in KUBECONFIG.
// Kubeconfig merging takes each named entry whole from the first file that
// has it, so the overlay holds:
//
//   - the current context and the context entry, with the namespace;
//   - the cluster entry, when an earlier file in sources has one with the
//     same name;
//   - the user entry with all its credentials, when impersonation is
//     requested (an entry with only impersonation fields would hide the
//     credentials) or when an earlier file has one with the same name.
func RenderOverlay(c Context, sources []Source) (*clientcmdapi.Config, error) {
	if c.Config == nil {
		return nil, fmt.Errorf("kubeconfig for context %q is not loaded", c.Context)
	}
	src := c.Config.DeepCopy()
	if err := clientcmd.ResolveLocalPaths(src); err != nil {
		return nil, fmt.Errorf("resolving kubeconfig paths: %w", err)
	}
	kctx := src.Contexts[c.Context]
	if kctx == nil {
		return nil, fmt.Errorf("context %q not found in %s", c.Context, displayPath(c.Kubeconfig))
	}
	out := clientcmdapi.NewConfig()
	out.CurrentContext = c.Context
	if ns := strings.TrimSpace(c.Namespace); ns != "" {
		kctx.Namespace = ns
	}
	out.Contexts[c.Context] = kctx

	owner := func(has func(*clientcmdapi.Config) bool) string {
		for _, s := range sources {
			if s.Config != nil && s.Path != "" && has(s.Config) {
				return s.Path
			}
		}
		return ""
	}
	ours := func(path string) bool { return c.Kubeconfig != "" && sameFile(path, c.Kubeconfig) }

	if name := kctx.Cluster; name != "" {
		if cl := src.Clusters[name]; cl != nil {
			if !ours(owner(func(cfg *clientcmdapi.Config) bool { return cfg.Clusters[name] != nil })) {
				out.Clusters[name] = cl
			}
		}
	}
	if name := kctx.AuthInfo; name != "" {
		auth := src.AuthInfos[name]
		shadowed := !ours(owner(func(cfg *clientcmdapi.Config) bool { return cfg.AuthInfos[name] != nil }))
		switch {
		case auth == nil && !c.Impersonation.IsZero():
			return nil, fmt.Errorf("context %q has no user to impersonate with", c.Context)
		case auth != nil && (shadowed || !c.Impersonation.IsZero()):
			applyImpersonation(auth, c.Impersonation)
			out.AuthInfos[name] = auth
		}
	} else if !c.Impersonation.IsZero() {
		return nil, fmt.Errorf("context %q has no user to impersonate with", c.Context)
	}
	return out, nil
}

// applyImpersonation sets the requested fields and keeps the others, the
// way kc's own client applies --as, --as-uid and --as-group.
func applyImpersonation(auth *clientcmdapi.AuthInfo, imp Impersonation) {
	imp = imp.Normalized()
	if imp.User != "" {
		auth.Impersonate = imp.User
	}
	if imp.UID != "" {
		auth.ImpersonateUID = imp.UID
	}
	if len(imp.Groups) > 0 {
		auth.ImpersonateGroups = imp.Groups
	}
}

func displayPath(p string) string {
	if p == "" {
		return "kubeconfig"
	}
	return p
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// makePrivateDir creates a 0700 directory for this process. It prefers
// <tmp>/kc-<uid>/<pid>-*, which lets a later kc remove what a crashed one
// left. When that root is not a private directory of this user (another
// user may have created it in a shared /tmp), it falls back to a fresh
// <tmp>/kc-* directory.
func makePrivateDir() (string, error) {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("kc-%d", os.Getuid()))
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return os.MkdirTemp("", "kc-")
	}
	if !privateDir(root) {
		return os.MkdirTemp("", "kc-")
	}
	reap(root)
	return os.MkdirTemp(root, fmt.Sprintf("%d-", os.Getpid()))
}

// privateDir reports whether path is a directory (not a symlink) owned by
// this user, tightening its mode to 0700 when an older kc made it 0755.
func privateDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return false
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o700) == nil
	}
	return true
}

// reap removes directories of kc processes that no longer run.
func reap(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pidStr, _, _ := strings.Cut(entry.Name(), "-")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}
