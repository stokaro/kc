package appconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	yaml "sigs.k8s.io/yaml"
)

// LoadError reports a configuration file that exists but cannot be read or
// parsed. Store.Update returns it, too, when it refuses to write over such a
// file.
type LoadError struct {
	Path string
	Err  error
}

func (e *LoadError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }
func (e *LoadError) Unwrap() error { return e.Err }

// Load reads ~/.kc/config.yaml. It returns the defaults when the file does
// not exist, and the defaults with a *LoadError when the file cannot be read
// or parsed.
func Load() (*Config, error) {
	p, err := DefaultPath()
	if err != nil {
		return Default(), &LoadError{Path: "~/.kc/config.yaml", Err: err}
	}
	return NewStore(p).Load()
}

// Store reads and writes one configuration file. A Store never writes over a
// file that does not load: Update reads the file again, refuses when it still
// fails, and otherwise changes only the fields its caller sets.
type Store struct {
	path string

	mu      sync.Mutex // serializes Update's read, change and write
	loadErr error
}

// NewStore returns a Store for the file at path. The file need not exist.
func NewStore(path string) *Store { return &Store{path: path} }

// Path returns the configuration file path.
func (s *Store) Path() string { return s.path }

// LoadErr returns the error of the last Load, or nil when it succeeded or the
// file did not exist.
func (s *Store) LoadErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// Load reads the file. A missing file yields the defaults and no error. A file
// that cannot be read or parsed yields the defaults and a *LoadError; nothing
// of that file is used, so a partly valid file never mixes with defaults.
func (s *Store) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, _, err := s.read()
	s.loadErr = err
	if err != nil {
		return Default(), err
	}
	return cfg, nil
}

// Update reads the file again, applies change to it and writes the result
// atomically. It fails with a *LoadError, and leaves the file untouched,
// when the file on disk cannot be read or parsed. Keys kc does not know are
// kept. Update returns the configuration it wrote.
func (s *Store) Update(change func(*Config)) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, raw, err := s.read()
	if err != nil {
		return nil, err
	}
	change(cfg)
	normalizeForSave(cfg)
	data, err := marshalOver(raw, cfg)
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", s.path, err)
	}
	if err := writeFileAtomic(s.path, data); err != nil {
		return nil, err
	}
	return cfg, nil
}

// read parses the file into a normalized Config and into a generic map that
// keeps keys kc does not know. A missing file is not an error.
func (s *Store) read() (*Config, map[string]any, error) {
	if s.path == "" {
		return nil, nil, &LoadError{Path: "~/.kc/config.yaml", Err: errors.New("the configuration path is unknown")}
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil, nil
	}
	if err != nil {
		return nil, nil, &LoadError{Path: s.path, Err: err}
	}
	cfg := Default()
	if err := unmarshalOverDefaults(data, cfg); err != nil {
		return nil, nil, &LoadError{Path: s.path, Err: err}
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, &LoadError{Path: s.path, Err: err}
	}
	normalizeLoaded(cfg)
	return cfg, raw, nil
}

// marshalOver encodes cfg on top of raw, the file as it was read, so keys kc
// does not know survive. Maps merge key by key, matching keys the way the
// decoder does (ignoring case); every other value comes from cfg.
func marshalOver(raw map[string]any, cfg *Config) ([]byte, error) {
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := yaml.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if raw == nil {
		return encoded, nil
	}
	return yaml.Marshal(mergeMaps(raw, fields))
}

func mergeMaps(dst, src map[string]any) map[string]any {
	for k, v := range src {
		old, found := dst[k]
		if !found {
			for dk, dv := range dst {
				if strings.EqualFold(dk, k) {
					// The file spells the key differently; write it the way
					// kc does and drop the old spelling.
					old, found = dv, true
					delete(dst, dk)
					break
				}
			}
		}
		if sub, ok := v.(map[string]any); ok && found {
			if oldSub, ok := old.(map[string]any); ok {
				v = mergeMaps(oldSub, sub)
			}
		}
		dst[k] = v
	}
	return dst
}

// writeFileAtomic replaces path with data through a temporary file in the
// same directory, so a failed write never leaves a truncated file. It keeps
// the mode of the file it replaces and writes through a symbolic link to its
// target instead of replacing the link.
func writeFileAtomic(path string, data []byte) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", target, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", target, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", target, err)
	}
	return nil
}
