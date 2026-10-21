package conceptslifecycle_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// fakeConnector stands in for a database that can become unreachable.
type fakeConnector struct{ down *atomic.Bool }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	if c.down.Load() {
		return nil, errors.New("database unreachable")
	}
	return &fakeConn{down: c.down}, nil
}

func (c fakeConnector) Driver() driver.Driver { return fakeDriver(c) }

type fakeDriver fakeConnector

func (d fakeDriver) Open(string) (driver.Conn, error) {
	return fakeConnector(d).Connect(context.Background())
}

type fakeConn struct{ down *atomic.Bool }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *fakeConn) Ping(context.Context) error {
	if c.down.Load() {
		return errors.New("database unreachable")
	}
	return nil
}

// Docs: concepts/lifecycle.mdx#health-and-readiness
// "**liveness** fails only for a broken state a restart would fix, so `gas/database` does not ping there ... **Readiness** fails while a dependency is unreachable"
func TestDatabaseLivenessIgnoresOutageReadinessDoesNot(t *testing.T) {
	t.Parallel()
	down := &atomic.Bool{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConnector(fakeConnector{down: down}))),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })

	down.Store(true)
	health := w.CheckHealth(t.Context())
	ready := w.CheckReady(t.Context())
	if err, ok := health["gas/database"]; !ok || err != nil {
		t.Fatalf("CheckHealth during an outage = %v, want gas/database present and nil", health)
	}
	if err, ok := ready["gas/database"]; !ok || err == nil {
		t.Fatalf("CheckReady during an outage = %v, want gas/database present with an error", ready)
	}
}
