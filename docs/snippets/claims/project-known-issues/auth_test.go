package projectknownissues_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" database/sql driver
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

func newMigrations(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	svc, err := migrate.New()(db)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

func sqliteDB(t *testing.T) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = filepath.Join(t.TempDir(), "auth.db")
	cfg.Database.MaxOpenConns = 1
	cfg.Database.MaxIdleConns = 1
	svc, err := database.New(database.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// apikeyOnSQLite builds and migrates an apikey service on SQLite.
func apikeyOnSQLite(t *testing.T, opts []apikey.Option, cfg gas.ConfigProvider) (*apikey.Service, error) {
	t.Helper()
	db := sqliteDB(t)
	mig := newMigrations(t, db)
	if cfg == nil {
		cfg = &configtest.MockConfig{}
	}
	svc, err := apikey.New(opts...)(db, nopLogger(), mig, cfg)
	if err != nil {
		t.Fatalf("apikey.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		return nil, err
	}
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return svc, nil
}

func withKeyLength(n int) *apikey.Config {
	cfg := apikey.DefaultConfig()
	cfg.APIKey.KeyLength = n
	return cfg
}

// Docs: project/known-issues.mdx#auth
// "`Generate` slices the generated key at the prefix length plus 8
// characters. With `KeyLength` below 6 bytes the encoded key is shorter than
// that, and the slice panics." and "a config given with `apikey.WithConfig` is
// used as given."
func TestKI040ShortKeyLengthPanics(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 5} {
		svc, err := apikeyOnSQLite(t, []apikey.Option{apikey.WithConfig(withKeyLength(n))}, nil)
		if err != nil {
			t.Fatalf("KeyLength %d with WithConfig: Init error = %v, want the config used as given", n, err)
		}
		v := panicValue(func() { _, _, _ = svc.Generate(t.Context(), "u1", "k", nil) })
		if v == nil || !strings.Contains(fmt.Sprint(v), "slice bounds out of range") {
			t.Errorf("KeyLength %d: Generate panic = %v, want a slice bounds panic", n, v)
		}
	}
	// At 6 bytes the encoded key reaches 8 characters, so the slice holds.
	svc, err := apikeyOnSQLite(t, []apikey.Option{apikey.WithConfig(withKeyLength(6))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := panicValue(func() { _, _, _ = svc.Generate(t.Context(), "u1", "k", nil) }); v != nil {
		t.Errorf("KeyLength 6: Generate panicked with %v, want the boundary at 6", v)
	}
}

// Docs: project/known-issues.mdx#auth
// "A config bound from the `ConfigProvider` is protected by its
// `validate:"min=16"` tag"
func TestKI040BoundConfigValidated(t *testing.T) {
	t.Parallel()
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"apikey": map[string]any{"keyLength": 4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apikeyOnSQLite(t, nil, cfg); err == nil {
		t.Fatalf("Init with a bound KeyLength of 4 error = nil, want validation to reject it")
	}
	cfg16, err := configtest.NewMockConfigWithValues(map[string]any{"apikey": map[string]any{"keyLength": 16}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apikeyOnSQLite(t, nil, cfg16); err != nil {
		t.Fatalf("Init with a bound KeyLength of 16 error = %v, want accepted", err)
	}
}

// Docs: project/known-issues.mdx#auth
// "**Workaround:** keep `KeyLength` at 16 or more in a config passed with
// `WithConfig`, as the bound config requires."
func TestKI040Workaround(t *testing.T) {
	t.Parallel()
	svc, err := apikeyOnSQLite(t, []apikey.Option{apikey.WithConfig(withKeyLength(16))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, info, err := svc.Generate(t.Context(), "u1", "k", nil)
	if err != nil || key == "" || info == nil {
		t.Fatalf("Generate with KeyLength 16 = %q, %v, %v, want a key", key, info, err)
	}
}

// regexpScopes matches the scopes column declaration the page quotes.
var regexpScopes = regexp.MustCompile(`scopes\s+TEXT\s+NOT NULL DEFAULT ''`)

// mysqlImage is the MySQL release the claim is about, pinned by digest so a
// retagged image cannot change the behavior under test.
const mysqlImage = "mysql:8.4@sha256:0f51e63e537a1f176423fa03ef559aabe6fa4372a53160a44bc7e75da7d15364"

var (
	myOnce sync.Once
	myCtr  *mysql.MySQLContainer
	myErr  error
	mySeq  atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if myCtr != nil {
		_ = myCtr.Terminate(context.Background())
	}
	os.Exit(code)
}

// mysqlDSN returns a DSN for a fresh database on the shared MySQL container,
// with params as its query string.
func mysqlDSN(t *testing.T, params string) string {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	myOnce.Do(func() {
		myCtr, myErr = mysql.Run(ctx, mysqlImage,
			mysql.WithDatabase("testdb"), mysql.WithUsername("root"), mysql.WithPassword("testpass"))
	})
	if myErr != nil {
		t.Fatalf("start mysql: %v", myErr)
	}
	admin, err := myCtr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	adminDB, err := sql.Open("mysql", admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adminDB.Close() }()
	name := fmt.Sprintf("ki_%d", mySeq.Add(1))
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	dsn := admin[:strings.LastIndex(admin, "/")+1] + name
	if params != "" {
		dsn += "?" + params
	}
	return dsn
}

// sqlProvider is a gas.DatabaseProvider over a *sql.DB whose Driver() is
// "mysql".
type sqlProvider struct{ db *sql.DB }

func (p *sqlProvider) DB() *sql.DB                    { return p.db }
func (p *sqlProvider) Driver() string                 { return "mysql" }
func (p *sqlProvider) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }
func (p *sqlProvider) Query(ctx context.Context, q string, args ...any) (gas.Rows, error) {
	return p.db.QueryContext(ctx, q, args...)
}
func (p *sqlProvider) Exec(ctx context.Context, q string, args ...any) (gas.Result, error) {
	return p.db.ExecContext(ctx, q, args...)
}
func (p *sqlProvider) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, opts)
}
func (p *sqlProvider) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func openMySQL(t *testing.T, params string) gas.DatabaseProvider {
	t.Helper()
	db, err := sql.Open("mysql", mysqlDSN(t, params))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	return &sqlProvider{db: db}
}

// migrateStore builds one auth store on db and returns the error from applying
// its migrations.
func migrateStore(t *testing.T, store string, db gas.DatabaseProvider) error {
	t.Helper()
	mig := newMigrations(t, db)
	var (
		svc gas.Service
		err error
	)
	switch store {
	case "session":
		svc, err = session.New(session.WithConfig(session.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	case "apikey":
		svc, err = apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	case "token":
		svc, err = token.New(token.WithConfig(token.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	}
	if err != nil {
		t.Fatalf("%s.New: %v", store, err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("%s Init: %v", store, err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return mig.RunPending()
}

// Docs: project/known-issues.mdx#auth
// "The migration `create api keys` (version `20250323002`) declares
// `scopes TEXT NOT NULL DEFAULT ... and MySQL rejects a literal default on a
// `TEXT` column with `Error 1101 (42000)`. The session and token stores
// migrate cleanly."
func TestKI044APIKeyMigrationFailsOnMySQL(t *testing.T) {
	t.Parallel()
	src := readFile(t, filepath.Join(repoRoot(t), "auth", "apikey", "db", "mysql", "20250323002_create_api_keys.up.sql"))
	if !regexpScopes.MatchString(src) {
		t.Fatalf("20250323002_create_api_keys.up.sql does not declare scopes TEXT NOT NULL DEFAULT ''")
	}
	const params = "multiStatements=true&parseTime=true"
	err := migrateStore(t, "apikey", openMySQL(t, params))
	if err == nil || !strings.Contains(err.Error(), "Error 1101 (42000)") {
		t.Fatalf("apikey migrations on MySQL 8.4 error = %v, want Error 1101 (42000)", err)
	}
	for _, store := range []string{"session", "token"} {
		if err := migrateStore(t, store, openMySQL(t, params)); err != nil {
			t.Errorf("%s migrations on MySQL 8.4 error = %v, want clean", store, err)
		}
	}
}

// Docs: project/known-issues.mdx#auth
// "each auth MySQL migration file holds several statements and `gas/migrate`
// runs a file in one call, so on MySQL the DSN needs `multiStatements=true` for
// any of them to apply."
func TestKI044MySQLNeedsMultiStatements(t *testing.T) {
	t.Parallel()
	for _, store := range []string{"session", "apikey", "token"} {
		if err := migrateStore(t, store, openMySQL(t, "parseTime=true")); err == nil {
			t.Errorf("%s migrations without multiStatements=true error = nil, want a failure", store)
		}
	}
}

// Docs: project/known-issues.mdx#auth
// "**Workaround:** for API keys, use PostgreSQL or SQLite."
// SQLite only: the apikey store migrates and generates a key there.
// PostgreSQL is not run here.
func TestKI044WorkaroundSQLite(t *testing.T) {
	t.Parallel()
	svc, err := apikeyOnSQLite(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Generate(t.Context(), "u1", "k", nil); err != nil {
		t.Fatalf("Generate on SQLite: %v", err)
	}
}
