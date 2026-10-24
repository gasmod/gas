package modulesconfig_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas/config"
)

// mapProvider is a provider built from Name and Load alone.
type mapProvider struct {
	name   string
	values map[string]any
}

func (p mapProvider) Name() string                  { return p.name }
func (p mapProvider) Load() (map[string]any, error) { return p.values, nil }

// countingProvider counts its loads and returns a copy of values each time.
type countingProvider struct {
	name   string
	values map[string]any
	loads  atomic.Int32
}

func (p *countingProvider) Name() string { return p.name }
func (p *countingProvider) Load() (map[string]any, error) {
	p.loads.Add(1)
	out := make(map[string]any, len(p.values))
	for k, v := range p.values {
		out[k] = v
	}
	return out, nil
}

var errBoom = errors.New("boom")

// failingProvider always fails to load.
type failingProvider struct {
	name  string
	loads atomic.Int32
}

func (p *failingProvider) Name() string { return p.name }
func (p *failingProvider) Load() (map[string]any, error) {
	p.loads.Add(1)
	return nil, errBoom
}

// journal records events in order, from any goroutine.
type journal struct {
	mu     sync.Mutex
	events []string
}

func (j *journal) add(e string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, e)
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.events...)
}

// journalProvider records its load in a journal.
type journalProvider struct {
	name   string
	j      *journal
	values map[string]any
}

func (p journalProvider) Name() string { return p.name }
func (p journalProvider) Load() (map[string]any, error) {
	p.j.add("load " + p.name)
	return p.values, nil
}

// journalExtension records its hooks in a journal and fails the hooks named
// in failPre or failPost.
type journalExtension struct {
	name     string
	j        *journal
	failPre  bool
	failPost bool
}

func (e journalExtension) Name() string { return e.name }

func (e journalExtension) PreLoad(context.Context, *config.Config) error {
	e.j.add("pre " + e.name)
	if e.failPre {
		return errBoom
	}
	return nil
}

func (e journalExtension) PostLoad(context.Context, *config.Config) error {
	e.j.add("post " + e.name)
	if e.failPost {
		return errBoom
	}
	return nil
}

func load(t *testing.T, opts ...config.Option) *config.Config {
	t.Helper()
	cfg := config.New(opts...)
	if err := cfg.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func values(name string, v map[string]any) config.Option {
	return config.WithProvider(mapProvider{name: name, values: v})
}
