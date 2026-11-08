package config_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

type m = map[string]any

// static is a Provider that returns its values, or its error, and counts
// its loads.
type static struct {
	err    error
	values map[string]any
	name   string
	mu     sync.Mutex
	loads  int
}

func newStatic(name string, values map[string]any) *static {
	return &static{name: name, values: values}
}

func (s *static) Name() string { return s.name }

func (s *static) Load() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loads++
	if s.err != nil {
		return nil, s.err
	}

	return s.values, nil
}

func (s *static) set(values map[string]any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.values, s.err = values, err
}

func (s *static) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loads
}

// withCtx is a ContextProvider that records the context of its last load.
type withCtx struct {
	*static

	ctx context.Context //nolint:containedctx // recorded for the test to inspect
}

func (p *withCtx) LoadContext(ctx context.Context) (map[string]any, error) {
	p.ctx = ctx

	return p.Load()
}

// newConfig returns a Config with one static provider holding values, and
// loads it.
func newConfig(t *testing.T, values map[string]any) *config.Config {
	t.Helper()

	c := config.New(config.WithProvider(newStatic("static", values)))
	require.NoError(t, c.Load())

	return c
}

// recorder is an Extension that appends its hooks to a shared log, runs
// fn in them, and returns its errors.
type recorder struct {
	preErr  error
	postErr error
	fn      func(hook string, cfg *config.Config)
	log     *[]string
	name    string
}

func (r *recorder) Name() string { return r.name }

func (r *recorder) PreLoad(_ context.Context, cfg *config.Config) error {
	*r.log = append(*r.log, r.name+".pre")
	if r.fn != nil {
		r.fn("pre", cfg)
	}

	return r.preErr
}

func (r *recorder) PostLoad(_ context.Context, cfg *config.Config) error {
	*r.log = append(*r.log, r.name+".post")
	if r.fn != nil {
		r.fn("post", cfg)
	}

	return r.postErr
}

// chdir makes a fresh directory the working directory for the test, with
// symlinks resolved, and returns it. Tests that use it cannot run in
// parallel.
func chdir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(dir)

	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
