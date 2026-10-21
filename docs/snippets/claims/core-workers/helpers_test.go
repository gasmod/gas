package coreworkers_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas"
)

// list is a goroutine-safe string slice.
type list struct {
	mu    sync.Mutex
	items []string
}

func (l *list) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, fmt.Sprintf(format, args...))
}

func (l *list) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.items)
}

// svc is a singleton service whose Init and Close run the given funcs and
// record themselves. Each K is its own registration type.
type svc[K any] struct {
	name    string
	rec     *list
	onInit  func(ctx context.Context) error
	onClose func(ctx context.Context) error
	inits   atomic.Int32
	closes  atomic.Int32
}

func (s *svc[K]) Name() string { return s.name }

func (s *svc[K]) Init(ctx context.Context) error {
	s.inits.Add(1)
	if s.rec != nil {
		s.rec.add("init %s", s.name)
	}
	if s.onInit != nil {
		return s.onInit(ctx)
	}
	return nil
}

func (s *svc[K]) Close(ctx context.Context) error {
	s.closes.Add(1)
	if s.rec != nil {
		s.rec.add("close %s", s.name)
	}
	if s.onClose != nil {
		return s.onClose(ctx)
	}
	return nil
}

// instance registers s as *svc[K].
func instance[K any](s *svc[K]) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func() *svc[K] { return s })
}

type (
	kA struct{}
	kB struct{}
)

// migrations stands in for the migration manager and records when it runs.
type migrations struct {
	gas.MigrationManager
	rec *list
}

func (*migrations) Name() string                { return "migrations" }
func (*migrations) Init(context.Context) error  { return nil }
func (*migrations) Close(context.Context) error { return nil }
func (m *migrations) RunPending() error {
	m.rec.add("migrations")
	return nil
}
