package modulesmigrations_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// repoRoot is the repository root, relative to this package.
const repoRoot = "../../../.."

// migratingService registers its migrations during Init and records whether
// it was closed.
type migratingService struct {
	name   string
	mgr    gas.MigrationManager
	list   []gas.Migration
	closed atomic.Bool
}

func (s *migratingService) Name() string { return s.name }

func (s *migratingService) Init(context.Context) error {
	s.mgr.RegisterSlice(s, s.list)
	return nil
}

func (s *migratingService) Close(context.Context) error {
	s.closed.Store(true)
	return nil
}

// Distinct types so two migrating services can be registered at once.
type (
	alphaService migratingService
	betaService  migratingService
)

func (s *alphaService) Name() string                    { return (*migratingService)(s).Name() }
func (s *alphaService) Init(ctx context.Context) error  { return (*migratingService)(s).Init(ctx) }
func (s *alphaService) Close(ctx context.Context) error { return (*migratingService)(s).Close(ctx) }
func (s *betaService) Name() string                     { return (*migratingService)(s).Name() }
func (s *betaService) Init(ctx context.Context) error   { return (*migratingService)(s).Init(ctx) }
func (s *betaService) Close(ctx context.Context) error  { return (*migratingService)(s).Close(ctx) }

// runner is what gas.App and gas.Worker have in common for these tests.
type runner interface {
	Start(ctx context.Context) error
	ReadyFunc(fn func(*gas.ServiceContainer) error)
}

// appStack returns the options for a config, a logger, an SQLite database
// with the runlog tables, and a migration manager.
func appStack(t *testing.T, log gas.Logger) (*database.Service, []gas.Option) {
	t.Helper()
	db := newSQLite(t)
	if log == nil {
		log = &gas.NopLogger{}
	}
	return db, []gas.Option{
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](log),
		gas.WithServiceInstance[gas.DatabaseProvider](gas.DatabaseProvider(db)),
	}
}

// start builds an App or a Worker from opts and starts it, stopping it at
// the end of the test.
func start(t *testing.T, kind string, opts []gas.Option, ready func(*gas.ServiceContainer) error) (runner, error) {
	t.Helper()
	var r runner
	switch kind {
	case "App":
		app := gas.NewApp(opts...)
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		r = app
	default:
		w := gas.NewWorker(opts...)
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		r = w
	}
	if ready != nil {
		r.ReadyFunc(ready)
	}
	return r, r.Start(t.Context())
}

// Docs: modules/migrations.mdx#what-it-is-for
// "Each service registers its own while it initializes, so a service carries
// its schema with it, and removing a service removes its migrations. Once
// every service has initialized, the app applies whatever is pending, from
// every service, in one global version order."
// and
// Docs: modules/migrations.mdx#declaring-migrations
// "A service takes `gas.MigrationManager` as a constructor parameter and
// registers its migrations in `Init`."
// and
// Docs: modules/migrations.mdx#when-migrations-run
// "`gas.App` and `gas.Worker` both run them as part of `Start` (and so of
// `App.Run`): after every service has initialized, before the app's ready
// hooks"
// Assumption: "removing a service removes its migrations" is checked as a
// service left out of the app having none of its migrations applied.
func TestStartRunsMigrationsInGlobalOrder(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"App", "Worker"} {
		for _, withBeta := range []bool{true, false} {
			db, opts := appStack(t, nil)
			opts = append(opts,
				gas.WithSingletonService[gas.MigrationManager](migrate.New()),
				gas.WithSingletonService[*alphaService](func(m gas.MigrationManager) (*alphaService, error) {
					return &alphaService{name: "alpha", mgr: m, list: []gas.Migration{logMig("002"), logMig("004")}}, nil
				}),
			)
			if withBeta {
				opts = append(opts, gas.WithSingletonService[*betaService](func(m gas.MigrationManager) (*betaService, error) {
					return &betaService{name: "beta", mgr: m, list: []gas.Migration{logMig("001"), logMig("003")}}, nil
				}))
			}
			var atReady []string
			_, err := start(t, kind, opts, func(*gas.ServiceContainer) error {
				atReady = logged(t, db.DB(), "runlog")
				return nil
			})
			if err != nil {
				t.Fatalf("%s Start: %v", kind, err)
			}
			want := []string{"002", "004"}
			if withBeta {
				want = []string{"001", "002", "003", "004"}
			}
			if !slices.Equal(atReady, want) {
				t.Errorf("%s with beta=%v: applied by the time ready hooks ran = %v, want %v", kind, withBeta, atReady, want)
			}
		}
	}
}

// Docs: modules/migrations.mdx#declaring-migrations
// "Register before `Init` returns: pending migrations are applied right after
// the last service initializes."
// and
// Docs: modules/migrations.mdx#health-and-readiness
// "Once the app has started, the last case means a migration was registered or
// reverted after `RunPending` ran."
func TestLateRegistrationStaysPending(t *testing.T) {
	t.Parallel()
	db, opts := appStack(t, nil)
	opts = append(opts, gas.WithSingletonService[gas.MigrationManager](migrate.New()))
	_, err := start(t, "Worker", opts, func(c *gas.ServiceContainer) error {
		m, err := c.Resolve[gas.MigrationManager]()
		if err != nil {
			return err
		}
		m.Register(&owner{name: "late"}, logMig("001"))
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := logged(t, db.DB(), "runlog"); len(got) != 0 {
		t.Errorf("migration registered after RunPending ran: applied %v, want pending", got)
	}
	m := newMigrator(t, db)
	m.Register(&owner{name: "late"}, logMig("001"))
	if err := m.CheckReady(t.Context()); err == nil || !strings.Contains(err.Error(), "1 pending migration(s)") {
		t.Errorf("CheckReady = %v, want 1 pending migration(s)", err)
	}
}

// Docs: modules/migrations.mdx#transactions-and-failures
// "If `RunPending` fails, `Start` closes every service it initialized and
// returns an error starting with `gas: migrations:`. The app does not start."
func TestStartFailsOnMigrationError(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"App", "Worker"} {
		db, opts := appStack(t, nil)
		svc := &alphaService{name: "alpha", list: []gas.Migration{{
			Version: "001", Description: "bad", Up: "INSERT INTO missing_table VALUES (1)", Down: "SELECT 1",
		}}}
		opts = append(opts,
			gas.WithSingletonService[gas.MigrationManager](migrate.New()),
			gas.WithSingletonService[*alphaService](func(m gas.MigrationManager) (*alphaService, error) {
				svc.mgr = m
				return svc, nil
			}),
		)
		readyRan := false
		_, err := start(t, kind, opts, func(*gas.ServiceContainer) error { readyRan = true; return nil })
		if err == nil || !strings.HasPrefix(err.Error(), "gas: migrations:") {
			t.Errorf("%s Start = %v, want an error starting with gas: migrations:", kind, err)
		}
		if !svc.closed.Load() {
			t.Errorf("%s: initialized service not closed after the migration failure", kind)
		}
		if readyRan {
			t.Errorf("%s: ready hooks ran after the migration failure", kind)
		}
		_ = db
	}
}

// Docs: modules/migrations.mdx#wiring
// "so a database provider must be registered under that interface."
func TestNeedsDatabaseProvider(t *testing.T) {
	t.Parallel()
	_, err := start(t, "Worker", []gas.Option{
		gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{})),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
	}, nil)
	if err == nil {
		t.Fatal("Start with no gas.DatabaseProvider: err = nil, want a startup error")
	}
}

// Docs: modules/migrations.mdx#wiring
// "The app looks the manager up as `gas.MigrationManager`, and when none is
// registered it runs no migrations and reports nothing."
func TestNoManagerRunsNothing(t *testing.T) {
	t.Parallel()
	log := &recLogger{}
	db, opts := appStack(t, log)
	if _, err := start(t, "Worker", opts, nil); err != nil {
		t.Fatalf("Start with no migration manager: %v, want nil", err)
	}
	if tableExists(t, db.DB(), "__gas_migrations") {
		t.Error("__gas_migrations exists, want no migration work at all")
	}
	for _, msg := range log.messages() {
		if strings.Contains(strings.ToLower(msg), "migrat") {
			t.Errorf("logged %q, want nothing about migrations", msg)
		}
	}
}

// concreteUser registers a migration through the concrete manager type.
type concreteUser struct{ mgr *migrate.Service }

func (s *concreteUser) Name() string { return "concrete-user" }
func (s *concreteUser) Init(context.Context) error {
	s.mgr.Register(s, logMig("001"))
	return nil
}
func (s *concreteUser) Close(context.Context) error { return nil }

// Docs: modules/migrations.mdx#wiring
// "Registered as `*migrate.Service`, the manager is never found: no migration
// runs, and any service that takes a `gas.MigrationManager` fails to resolve,
// which fails startup."
func TestConcreteRegistration(t *testing.T) {
	t.Parallel()

	t.Run("NoMigrationRuns", func(t *testing.T) {
		t.Parallel()
		db, opts := appStack(t, nil)
		opts = append(opts,
			gas.WithSingletonService[*migrate.Service](migrate.New()),
			gas.WithSingletonService[*concreteUser](func(m *migrate.Service) (*concreteUser, error) {
				return &concreteUser{mgr: m}, nil
			}),
		)
		if _, err := start(t, "App", opts, nil); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if got := logged(t, db.DB(), "runlog"); len(got) != 0 {
			t.Fatalf("migrations applied = %v, want none", got)
		}
	})
	t.Run("InterfaceUserFails", func(t *testing.T) {
		t.Parallel()
		_, opts := appStack(t, nil)
		opts = append(opts,
			gas.WithSingletonService[*migrate.Service](migrate.New()),
			gas.WithSingletonService[*alphaService](func(m gas.MigrationManager) (*alphaService, error) {
				return &alphaService{name: "alpha", mgr: m}, nil
			}),
		)
		if _, err := start(t, "App", opts, nil); err == nil {
			t.Fatal("Start with a gas.MigrationManager dependency and only *migrate.Service registered: err = nil")
		}
	})
}

// Docs: modules/migrations.mdx#rolling-back
// "Nothing in Gas calls `Down`."
// Gas here is the framework modules: every Go module in the repository except
// the examples and the docs.
func TestNothingCallsDown(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if d.IsDir() {
			if rel == "example" || rel == "docs" || strings.HasPrefix(d.Name(), ".") && rel != "." || strings.HasSuffix(rel, "/examples") || rel == "config/examples" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), ".Down(") {
			t.Errorf("%s calls Down", rel)
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("walk: %v", err)
	}
}
