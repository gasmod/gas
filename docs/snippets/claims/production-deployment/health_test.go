package productiondeployment_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	cachemem "github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/queue/sqs"
	"github.com/gasmod/gas/ui"
)

// Docs: production/deployment.mdx#health-and-readiness
// "Gas does not mount health endpoints. It gives you the checks, and you
// choose the paths. Every app's container holds two aggregates:"
func TestEveryContainerHoldsTheAggregates(t *testing.T) {
	a := gas.NewApp(gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	if _, err := a.ServiceContainer().Resolve[gas.HealthProvider](); err != nil {
		t.Errorf("gas.HealthProvider: %v", err)
	}
	if _, err := a.ServiceContainer().Resolve[gas.ReadyProvider](); err != nil {
		t.Errorf("gas.ReadyProvider: %v", err)
	}
	if routes := a.Router().Routes(); len(routes) != 0 {
		t.Errorf("an app with no services has routes %v, want no health endpoints", routes)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	for _, path := range []string{"/healthz", "/readyz", "/health", "/ready"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, resp.StatusCode)
		}
	}
}

// reporter is a service distinct per kind K whose checks are optional.
// Only the reporting kinds below implement the interfaces.
type reporter[K any] struct {
	svc[K]
	check func(ctx context.Context) error
}

type healthOnly[K any] struct{ reporter[K] }

func (r *healthOnly[K]) CheckHealth(ctx context.Context) error { return r.check(ctx) }

type readyOnly[K any] struct{ reporter[K] }

func (r *readyOnly[K]) CheckReady(ctx context.Context) error { return r.check(ctx) }

type both[K any] struct{ reporter[K] }

func (r *both[K]) CheckHealth(ctx context.Context) error { return r.check(ctx) }
func (r *both[K]) CheckReady(ctx context.Context) error  { return r.check(ctx) }

type (
	kHealthy struct{}
	kSick    struct{}
	kReady   struct{}
	kPanics  struct{}
	kBoth    struct{}
	kPlain   struct{}
)

// Docs: production/deployment.mdx#health-and-readiness
// "`gas.HealthProvider`, whose `CheckHealth(ctx)` calls `CheckHealth` on every
// active service that implements `gas.HealthReporter`" and
// "`gas.ReadyProvider`, whose `CheckReady(ctx)` calls `CheckReady` on every
// active service that implements `gas.ReadyReporter`"
// Docs: production/deployment.mdx#health-and-readiness
// "Both run the checks concurrently and return a map from service name to
// result, `nil` meaning healthy or ready. Services that do not implement the
// reporter interface are left out of the map. A check that panics is reported
// as an error starting with `panic:` instead of crashing the process. Only
// active services are checked: a service closed with `CloseService`, or by
// shutdown, is no longer in the map."
func TestAggregates(t *testing.T) {
	sick := errors.New("lost connection")
	// The two healthy checks wait for each other, so they pass only when
	// they run at the same time.
	var arrived sync.WaitGroup
	arrived.Add(2)
	rendezvous := func(context.Context) error {
		arrived.Done()
		done := make(chan struct{})
		go func() { arrived.Wait(); close(done) }()
		select {
		case <-done:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("ran alone: the checks are not concurrent")
		}
	}
	nilCheck := func(context.Context) error { return nil }
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*healthOnly[kHealthy]](func() *healthOnly[kHealthy] {
			return &healthOnly[kHealthy]{reporter[kHealthy]{check: rendezvous}}
		}),
		gas.WithSingletonService[*healthOnly[kSick]](func() *healthOnly[kSick] {
			return &healthOnly[kSick]{reporter[kSick]{check: func(context.Context) error { return sick }}}
		}),
		gas.WithSingletonService[*readyOnly[kReady]](func() *readyOnly[kReady] {
			return &readyOnly[kReady]{reporter[kReady]{check: nilCheck}}
		}),
		gas.WithSingletonService[*readyOnly[kPanics]](func() *readyOnly[kPanics] {
			return &readyOnly[kPanics]{reporter[kPanics]{check: func(context.Context) error { panic("broken") }}}
		}),
		gas.WithSingletonService[*both[kBoth]](func() *both[kBoth] {
			return &both[kBoth]{reporter[kBoth]{check: rendezvous}}
		}),
		gas.WithSingletonService[*svc[kPlain]](func() *svc[kPlain] { return &svc[kPlain]{} }),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	hp := a.ServiceContainer().MustResolve[gas.HealthProvider]()
	rp := a.ServiceContainer().MustResolve[gas.ReadyProvider]()

	health := hp.CheckHealth(t.Context())
	if got := slices.Sorted(maps.Keys(health)); !slices.Equal(got, []string{"kBoth", "kHealthy", "kSick"}) {
		t.Errorf("health keys %v, want the health reporters only", got)
	}
	if health["kHealthy"] != nil || health["kBoth"] != nil {
		t.Errorf("healthy results %v and %v, want nil from checks run concurrently",
			health["kHealthy"], health["kBoth"])
	}
	if !errors.Is(health["kSick"], sick) {
		t.Errorf("kSick: %v, want its error", health["kSick"])
	}

	ready := rp.CheckReady(t.Context())
	if got := slices.Sorted(maps.Keys(ready)); !slices.Equal(got, []string{"kBoth", "kPanics", "kReady"}) {
		t.Errorf("ready keys %v, want the ready reporters only", got)
	}
	if ready["kReady"] != nil {
		t.Errorf("kReady: %v, want nil", ready["kReady"])
	}
	if e := ready["kPanics"]; e == nil || !strings.HasPrefix(e.Error(), "panic:") {
		t.Errorf("panicking check: %v, want an error starting with panic:", e)
	}

	if err := a.CloseService[*readyOnly[kReady]](t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := rp.CheckReady(t.Context())["kReady"]; ok {
		t.Error("a service closed with CloseService is still checked")
	}
	if err := a.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := hp.CheckHealth(t.Context()); len(got) != 0 {
		t.Errorf("after shutdown health is %v, want empty", got)
	}
	if got := rp.CheckReady(t.Context()); len(got) != 0 {
		t.Errorf("after shutdown ready is %v, want empty", got)
	}
}

// Docs: production/deployment.mdx#health-and-readiness
// "The `GET` endpoints pass cross-origin protection, so probes need no
// configuration."
// The endpoints mirror the page's health service; a cross-site POST is the
// control that shows the protection is on.
func TestHealthEndpointsPassCrossOriginProtection(t *testing.T) {
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*svc[kA]](func(r *gas.Router) *svc[kA] {
			s := &svc[kA]{}
			s.onInit = func(context.Context) error {
				r.Get(s, "/healthz", func(ctx gas.Context, p gas.HealthProvider) error {
					return ctx.JSON(http.StatusOK, map[string]int{"checks": len(p.CheckHealth(ctx))})
				})
				r.Get(s, "/readyz", func(ctx gas.Context, p gas.ReadyProvider) error {
					return ctx.JSON(http.StatusOK, map[string]int{"checks": len(p.CheckReady(ctx))})
				})
				r.Post(s, "/readyz", func(ctx gas.Context) error { return ctx.NoContent() })
				return nil
			}
			return s
		}),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	send := func(method, path string) int {
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://elsewhere.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if code := send(http.MethodGet, path); code != http.StatusOK {
			t.Errorf("cross-site GET %s: %d, want 200", path, code)
		}
	}
	if code := send(http.MethodPost, "/readyz"); code != http.StatusForbidden {
		t.Errorf("cross-site POST: %d, want 403 from the protection the GETs pass", code)
	}
}

// Docs: production/deployment.mdx#health-and-readiness
// "Among the modules, gas/database and the Valkey backend of gas/cache
// implement both reporters; gas/migrate, gas/ui, the SQS backend of
// gas/queue, and the SES backend of gas/email implement readiness."
// Assumption: the second group implements readiness only, and the memory
// cache backend, which the page leaves out, implements neither.
func TestModuleReporters(t *testing.T) {
	health := reflect.TypeFor[gas.HealthReporter]()
	ready := reflect.TypeFor[gas.ReadyReporter]()
	for _, c := range []struct {
		name          string
		typ           reflect.Type
		health, ready bool
	}{
		{"database", reflect.TypeFor[*database.Service](), true, true},
		{"cache/valkey", reflect.TypeFor[*valkey.Service](), true, true},
		{"migrate", reflect.TypeFor[*migrate.Service](), false, true},
		{"ui", reflect.TypeFor[*ui.Service](), false, true},
		{"queue/sqs", reflect.TypeFor[*sqs.Service](), false, true},
		{"email/ses", reflect.TypeFor[*ses.Service](), false, true},
		{"cache/memory", reflect.TypeFor[*cachemem.Service](), false, false},
	} {
		if got := c.typ.Implements(health); got != c.health {
			t.Errorf("%s implements HealthReporter = %v, want %v", c.name, got, c.health)
		}
		if got := c.typ.Implements(ready); got != c.ready {
			t.Errorf("%s implements ReadyReporter = %v, want %v", c.name, got, c.ready)
		}
	}
}

// Docs: production/deployment.mdx#health-and-readiness
// "A service of your own reports by implementing either interface:" and
// "func (s *Service) CheckReady(ctx context.Context) error // nil means ready"
func TestOwnServiceReports(t *testing.T) {
	notReady := errors.New("warming up")
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*readyOnly[kReady]](func() *readyOnly[kReady] {
			return &readyOnly[kReady]{reporter[kReady]{check: func(context.Context) error { return notReady }}}
		}),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	got := a.ServiceContainer().MustResolve[gas.ReadyProvider]().CheckReady(t.Context())
	if !errors.Is(got["kReady"], notReady) {
		t.Fatalf("CheckReady map %v, want kReady's error", got)
	}
}
