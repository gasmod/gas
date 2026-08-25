package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	config "github.com/gasmod/gas/config"
	database "github.com/gasmod/gas/database"
)

// fakeBehavior drives the errors returned by the fake driver below. A real
// database will not fail a rollback or commit on demand, so these paths are
// only reachable through an injected driver.
type fakeBehavior struct {
	mu sync.Mutex

	// failPings is the number of leading Ping calls that fail, used to
	// exercise connection retry.
	failPings int
	pings     int

	beginErr    error
	commitErr   error
	rollbackErr error
}

func (b *fakeBehavior) pingErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pings++
	if b.pings <= b.failPings {
		return errors.New("fake: connection refused")
	}
	return nil
}

func (b *fakeBehavior) pingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pings
}

type fakeConnector struct{ behavior *fakeBehavior }

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{behavior: c.behavior}, nil
}
func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake: Open is not supported, use the connector")
}

type fakeConn struct{ behavior *fakeBehavior }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: Prepare is not supported")
}
func (c *fakeConn) Close() error               { return nil }
func (c *fakeConn) Ping(context.Context) error { return c.behavior.pingErr() }

func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.behavior.beginErr != nil {
		return nil, c.behavior.beginErr
	}
	return &fakeTx{behavior: c.behavior}, nil
}

type fakeTx struct{ behavior *fakeBehavior }

func (t *fakeTx) Commit() error   { return t.behavior.commitErr }
func (t *fakeTx) Rollback() error { return t.behavior.rollbackErr }

// newFakeService wires a service onto the fake driver via WithConnector.
func newFakeService(t *testing.T, b *fakeBehavior, tune ...func(*database.Config)) *database.Service {
	t.Helper()

	cfg := database.DefaultConfig()
	cfg.Database.Driver = ""
	cfg.Database.DSN = ""
	for _, fn := range tune {
		fn(cfg)
	}

	s := database.New(
		database.WithConfig(cfg),
		database.WithConnector(&fakeConnector{behavior: b}),
	)(nil, gas.NewNopLogger()())

	if err := s.Init(); err != nil {
		t.Fatalf("Init on fake driver: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBeginTx_BeginFails(t *testing.T) {
	beginErr := errors.New("fake: cannot begin")
	s := newFakeService(t, &fakeBehavior{beginErr: beginErr})

	_, err := s.BeginTx(context.Background(), nil)
	if !errors.Is(err, beginErr) {
		t.Fatalf("err = %v, want it to wrap the begin error", err)
	}
}

func TestWithTx_BeginFails(t *testing.T) {
	beginErr := errors.New("fake: cannot begin")
	s := newFakeService(t, &fakeBehavior{beginErr: beginErr})

	called := false
	err := s.WithTx(context.Background(), nil, func(*sql.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, beginErr) {
		t.Fatalf("err = %v, want it to wrap the begin error", err)
	}
	if called {
		t.Error("fn should not run when the transaction cannot be started")
	}
}

func TestWithTx_CommitFails(t *testing.T) {
	commitErr := errors.New("fake: cannot commit")
	s := newFakeService(t, &fakeBehavior{commitErr: commitErr})

	err := s.WithTx(context.Background(), nil, func(*sql.Tx) error { return nil })
	if !errors.Is(err, commitErr) {
		t.Fatalf("err = %v, want it to wrap the commit error", err)
	}
}

func TestWithTx_RollbackFailureJoinedOntoError(t *testing.T) {
	rollbackErr := errors.New("fake: cannot roll back")
	s := newFakeService(t, &fakeBehavior{rollbackErr: rollbackErr})

	fnErr := errors.New("work failed")
	err := s.WithTx(context.Background(), nil, func(*sql.Tx) error { return fnErr })

	// Both the caller's failure and the failed cleanup must survive: losing
	// the rollback error would hide a connection left holding a transaction.
	if !errors.Is(err, fnErr) {
		t.Errorf("err = %v, want it to report fn's error", err)
	}
	if !errors.Is(err, rollbackErr) {
		t.Errorf("err = %v, want it to report the rollback failure", err)
	}
}

func TestWithTx_RollbackFailureDuringPanic(t *testing.T) {
	s := newFakeService(t, &fakeBehavior{rollbackErr: errors.New("fake: cannot roll back")})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic to propagate")
		}
		if r != "boom" {
			t.Errorf("recovered %v, want the original panic value", r)
		}
	}()

	// A failed rollback must not replace or mask the panic.
	_ = s.WithTx(context.Background(), nil, func(*sql.Tx) error {
		panic("boom")
	})
}

func TestInit_RetryThenSucceed(t *testing.T) {
	b := &fakeBehavior{failPings: 2}

	s := newFakeService(t, b, func(c *database.Config) {
		c.Database.ConnRetries = 5
		c.Database.ConnRetryInterval = time.Millisecond
	})

	if got := b.pingCount(); got != 3 {
		t.Errorf("ping count = %d, want 3 (two failures then a success)", got)
	}
	if s.DB() == nil {
		t.Error("DB() is nil after a successful retry")
	}
}

// fakeConfigProvider is a gas.ConfigProvider that binds a fixed Config, used
// to check that Init pulls settings from DI when WithConfig is absent.
type fakeConfigProvider struct {
	gas.ConfigProvider

	bound  *database.Config
	err    error
	called bool
}

func (p *fakeConfigProvider) Bind(dest any, _ ...config.BindOption) error {
	p.called = true
	if p.err != nil {
		return p.err
	}
	cfg, ok := dest.(*database.Config)
	if !ok {
		return errors.New("unexpected bind target")
	}
	*cfg = *p.bound
	return nil
}

func TestInit_BindsConfigFromProvider(t *testing.T) {
	bound := database.DefaultConfig()
	bound.Database.Driver = "sqlite"
	bound.Database.DSN = filepath.Join(t.TempDir(), "bound.db")

	provider := &fakeConfigProvider{bound: bound}

	s := database.New()(provider, gas.NewNopLogger()())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if !provider.called {
		t.Error("Init did not bind from the config provider")
	}
	if s.Driver() != database.DriverSQLite {
		t.Errorf("Driver() = %q, want the bound driver", s.Driver())
	}
}

func TestInit_BindFailure(t *testing.T) {
	provider := &fakeConfigProvider{err: errors.New("bind exploded")}

	s := database.New()(provider, gas.NewNopLogger()())
	if err := s.Init(); err == nil {
		t.Fatal("expected Init to fail when config binding fails")
	}
}

func TestInit_WithConfigSkipsProvider(t *testing.T) {
	provider := &fakeConfigProvider{err: errors.New("should never be called")}

	cfg := database.DefaultConfig()
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "explicit.db")

	s := database.New(database.WithConfig(cfg))(provider, gas.NewNopLogger()())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if provider.called {
		t.Error("WithConfig should suppress binding from the provider")
	}
}

func TestInit_ConnectorInPgxMode(t *testing.T) {
	// A driver.Connector is a database/sql concept: initPgx never looks at
	// one and builds its pool from the DSN alone. Accepting the pair would
	// silently drop the connector and, with no DSN to fall back on, leave
	// pgxpool to resolve its own defaults and fail on a connection the
	// caller never asked for.
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = ""

	s := database.New(
		database.WithConfig(cfg),
		database.WithConnector(&fakeConnector{behavior: &fakeBehavior{}}),
	)(nil, gas.NewNopLogger()())

	err := s.Init()
	if err == nil {
		t.Fatal("expected Init to reject a connector in pgx mode")
	}
	if !strings.Contains(err.Error(), "WithConnector") {
		t.Fatalf("err = %v, want it to name the unusable connector", err)
	}
}
