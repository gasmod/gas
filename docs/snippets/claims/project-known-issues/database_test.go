package projectknownissues_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
)

// failingConnector is a driver.Connector whose Close fails, so the *sql.DB
// opened on it fails to close.
type failingConnector struct{ closeErr error }

func (c failingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("not used")
}
func (c failingConnector) Driver() driver.Driver { return nil }
func (c failingConnector) Close() error          { return c.closeErr }

// setField sets an unexported field of svc. The pgx-mode state of a Service
// is set up this way because no public API makes closing its *sql.DB fail.
func setField(svc *database.Service, name string, v any) {
	f := reflect.ValueOf(svc).Elem().FieldByName(name)
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(v))
}

// pgxModeService returns a pgx-mode Service whose pool is a lazy pool on an
// unreachable address and whose *sql.DB closes with closeErr (nil: closes
// cleanly).
func pgxModeService(t *testing.T, closeErr error) (*database.Service, *pgxpool.Pool) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.Driver = database.DriverPgx
	svc, err := database.New(database.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	setField(svc, "pool", pool)
	setField(svc, "db", sql.OpenDB(failingConnector{closeErr: closeErr}))
	return svc, pool
}

// poolClosed reports whether pool has been closed: acquiring from a closed
// pool fails with "closed pool" instead of a dial error.
func poolClosed(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, err := pool.Acquire(ctx)
	if err == nil {
		c.Release()
		return false
	}
	return strings.Contains(err.Error(), "closed pool")
}

// Docs: project/known-issues.mdx#database
// "`database.Service.Close` returns as soon as closing the `*sql.DB` fails,
// before it closes the pgx pool, so in pgx mode the pool stays open."
func TestKI038CloseLeaksPool(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("sql close failed")
	svc, pool := pgxModeService(t, closeErr)
	if err := svc.Close(t.Context()); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want the *sql.DB close error", err)
	}
	if poolClosed(t, pool) {
		t.Fatalf("pool closed after a failed *sql.DB close, want it left open (KI-038)")
	}

	// Control: when the *sql.DB closes cleanly, the same check sees the pool
	// closed.
	ok, okPool := pgxModeService(t, nil)
	if err := ok.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !poolClosed(t, okPool) {
		t.Fatalf("pool open after a clean Close, so the check cannot tell")
	}
}

// Docs: project/known-issues.mdx#database
// "`CheckHealth` and `CheckReady` return errors starting with `database:`
// (`database: closed`, `database: not initialized`), while every other error
// of the package starts with the service name, `gas/database`."
func TestKI043HealthErrorPrefix(t *testing.T) {
	t.Parallel()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = filepath.Join(t.TempDir(), "h.db")
	svc, err := database.New(database.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name() != "gas/database" {
		t.Fatalf("Name() = %q, want gas/database", svc.Name())
	}
	if err := svc.CheckHealth(t.Context()); err == nil || err.Error() != "database: not initialized" {
		t.Errorf("CheckHealth before Init = %v, want database: not initialized", err)
	}
	if err := svc.Ping(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "gas/database") {
		t.Errorf("Ping before Init = %v, want an error starting with gas/database", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := svc.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.CheckHealth(t.Context()); err == nil || err.Error() != "database: closed" {
		t.Errorf("CheckHealth after Close = %v, want database: closed", err)
	}
	if err := svc.CheckReady(t.Context()); err == nil || err.Error() != "database: closed" {
		t.Errorf("CheckReady after Close = %v, want database: closed", err)
	}

	bad := database.DefaultConfig()
	bad.Database.Driver = "nope"
	svc2, err := database.New(database.WithConfig(bad))(&configtest.MockConfig{}, nopLogger())
	if err == nil {
		err = svc2.Init(t.Context())
	}
	if err == nil || !strings.HasPrefix(err.Error(), "gas/database") {
		t.Errorf("error for an invalid driver = %v, want it to start with gas/database", err)
	}
}
