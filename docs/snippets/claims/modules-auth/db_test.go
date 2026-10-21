package modulesauth_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/migrate/migratetest"
)

// roundTrip runs one session, API key and token round trip on fresh
// databases from open, each in its own subtest.
func roundTrip(t *testing.T, open func(t *testing.T) gas.DatabaseProvider, skip map[string]string) {
	t.Helper()
	t.Run("session", func(t *testing.T) {
		if msg, ok := skip["session"]; ok {
			t.Skip(msg)
		}
		sess := mustSession(t, open(t), nil, nil)
		r, _ := sessionRequest(t, sess, "u1", nil)
		if p, err := sess.Authenticate(t.Context(), r); err != nil || p.Subject() != "u1" {
			t.Errorf("session Authenticate = %v, %v", p, err)
		}
	})
	t.Run("apikey", func(t *testing.T) {
		if msg, ok := skip["apikey"]; ok {
			t.Skip(msg)
		}
		keys := mustAPIKey(t, open(t), nil)
		key, _ := generate(t, keys, "u1", []string{"read"})
		if p, err := keys.Authenticate(t.Context(), apiKeyRequest(key)); err != nil || p.Subject() != "u1" {
			t.Errorf("apikey Authenticate = %v, %v", p, err)
		}
	})
	t.Run("token", func(t *testing.T) {
		if msg, ok := skip["token"]; ok {
			t.Skip(msg)
		}
		toks := mustToken(t, open(t), nil)
		raw, err := toks.Issue(t.Context(), "u1", "verify", 0)
		if err != nil {
			t.Fatalf("token Issue: %v", err)
		}
		if sub, err := toks.Verify(t.Context(), raw, "verify"); err != nil || sub != "u1" {
			t.Errorf("token Verify = %q, %v", sub, err)
		}
	})
}

// Docs: modules/auth.mdx#databases
// "`session`, `apikey` and `token` choose their SQL dialect from the
// `gas.DatabaseProvider`'s `Driver()`:"
// and "| `postgres`, `pgx` | PostgreSQL |" and "| `sqlite`, `sqlite3` | SQLite |"
// and "Their tables are `__gas_auth_sessions`, `__gas_auth_api_keys` and
// `__gas_auth_tokens`."
// PostgreSQL needs Docker; MySQL is TestMySQLStores.
func TestDialects(t *testing.T) {
	t.Parallel()
	for i, mk := range allBackends(t) {
		t.Run(backendNames[i], func(t *testing.T) {
			t.Parallel()
			roundTrip(t, func(t *testing.T) gas.DatabaseProvider { return mk(t).open(t) }, nil)
		})
	}
	t.Run("sqlite3", func(t *testing.T) {
		t.Parallel()
		roundTrip(t, func(t *testing.T) gas.DatabaseProvider {
			db := sqliteDB(t)
			return &sqlProvider{db: db.DB(), driver: "sqlite3"}
		}, nil)
	})
	t.Run("tables", func(t *testing.T) {
		t.Parallel()
		db := sqliteDB(t)
		mustSession(t, db, nil, nil)
		mustAPIKey(t, db, nil)
		mustToken(t, db, nil)
		for _, table := range []string{"__gas_auth_sessions", "__gas_auth_api_keys", "__gas_auth_tokens"} {
			_ = rowCount(t, db.DB(), table)
		}
	})
}

// Docs: modules/auth.mdx#databases
// "Any other driver fails `Init` with `unsupported driver`."
// and
// Docs: modules/auth.mdx#errors
// "Other errors start with the service name (`gas/auth/jwt`,
// `gas/auth/session`, `gas/auth/apikey`, `gas/auth/token`)"
func TestUnsupportedDriver(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"session", "apikey", "token"} {
		p := &sqlProvider{db: sqliteDB(t).DB(), driver: "oracle"}
		// A mock manager, since gas/migrate rejects the driver too.
		mig := &migratetest.MockMigrationManager{}
		cfg := &configtest.MockConfig{}
		var svc gas.Service
		var err error
		switch kind {
		case "session":
			svc, err = session.New()(p, nopLogger(), mig, cfg)
		case "apikey":
			svc, err = apikey.New()(p, nopLogger(), mig, cfg)
		case "token":
			svc, err = token.New()(p, nopLogger(), mig, cfg)
		}
		if err != nil {
			t.Fatalf("%s.New: %v", kind, err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		err = svc.Init(t.Context())
		if err == nil || !strings.Contains(err.Error(), "unsupported driver") || !strings.HasPrefix(err.Error(), "gas/auth/"+kind+": ") {
			t.Errorf("%s Init with driver oracle = %v, want gas/auth/%s: ... unsupported driver", kind, err, kind)
		}
	}
}

// Docs: modules/auth.mdx#errors
// "Other errors start with the service name (`gas/auth/jwt`,
// `gas/auth/session`, `gas/auth/apikey`, `gas/auth/token`) and wrap the
// underlying database, file or parsing error."
// A closed database makes every store call fail.
func TestStoreErrorPrefixes(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	sess := mustSession(t, db, nil, nil)
	keys := mustAPIKey(t, db, nil)
	toks := mustToken(t, db, nil)
	if err := db.DB().Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	ctx := t.Context()
	_, sErr := sess.Create(ctx, "u", nil, httptest.NewRequest("GET", "/", nil))
	_, _, kErr := keys.Generate(ctx, "u", "n", nil)
	_, tErr := toks.Issue(ctx, "u", "p", time.Hour)
	for prefix, err := range map[string]error{"gas/auth/session: ": sErr, "gas/auth/apikey: ": kErr, "gas/auth/token: ": tErr} {
		if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "sql: database is closed") {
			t.Errorf("err = %v, want prefix %q around the database error", err, prefix)
		}
	}
}

// Docs: modules/auth.mdx#databases
// "`gas/database` accepts `postgres`, `pgx` and `sqlite` as its own drivers.
// For MySQL, give it a connector with `database.WithConnector` and set
// `database.driver` to `mysql`, or register your own `gas.DatabaseProvider`
// whose `Driver()` returns `mysql`"
// and "| `mysql` | MySQL |"
// and "With both parameters, `session` and `token` work on MySQL 8.4"
// The apikey store is skipped here under KI-044; TestAPIKeyFailsOnMySQL
// checks its current failure.
func TestMySQLStores(t *testing.T) {
	t.Parallel()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = "mysql"
	cfg.Database.DSN = "root:pw@tcp(127.0.0.1:1)/x"
	svc, err := database.New(database.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := svc.Init(t.Context()); err == nil || !strings.Contains(err.Error(), "Database.Driver") {
		t.Errorf("gas/database with driver mysql and no connector = %v, want the driver rejected", err)
	}
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			roundTrip(t, func(t *testing.T) gas.DatabaseProvider {
				return route.open(t, mysqlDSN(t, mysqlParams))
			}, map[string]string{"apikey": ki044})
		})
	}
}

// Docs: modules/auth.mdx#databases
// "The MySQL DSN needs two parameters: `multiStatements=true`, because each
// migration file holds several statements and runs as one statement batch, and
// `parseTime=true`, because the stores scan `DATETIME` columns into
// `time.Time`."
func TestMySQLParameters(t *testing.T) {
	t.Parallel()
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			for _, kind := range []string{"session", "apikey", "token"} {
				if err := runMigrations(t, kind, route.open(t, mysqlDSN(t, "parseTime=true"))); err == nil {
					t.Errorf("%s migrations without multiStatements: err = nil, want an error", kind)
				}
			}
			sess := mustSession(t, route.open(t, mysqlDSN(t, "multiStatements=true")), nil, nil)
			r, _ := sessionRequest(t, sess, "u1", nil)
			if _, err := sess.Authenticate(t.Context(), r); err == nil || !strings.Contains(err.Error(), "time.Time") {
				t.Errorf("session without parseTime: Authenticate = %v, want a time.Time scan error", err)
			}
			toks := mustToken(t, route.open(t, mysqlDSN(t, "multiStatements=true")), nil)
			raw, err := toks.Issue(t.Context(), "u1", "p", 0)
			if err != nil {
				t.Fatalf("token Issue: %v", err)
			}
			if _, err := toks.Verify(t.Context(), raw, "p"); err == nil || !strings.Contains(err.Error(), "time.Time") {
				t.Errorf("token without parseTime: Verify = %v, want a time.Time scan error", err)
			}
		})
	}
}

// Docs: modules/auth.mdx#databases
// "the `apikey` migration fails there: its table declares a literal default on
// a `TEXT` column, which MySQL rejects. Registering `apikey` on MySQL therefore
// fails startup when the migrations run."
// This asserts the behavior the page states for KI-044.
func TestAPIKeyFailsOnMySQL(t *testing.T) {
	t.Parallel()
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			db := route.open(t, mysqlDSN(t, mysqlParams))
			w := gas.NewWorker(
				gas.WithServiceInstance[gas.ConfigProvider](boundConfig(t, map[string]any{})),
				gas.WithServiceInstance[gas.Logger](nopLogger()),
				gas.WithServiceInstance[gas.DatabaseProvider](db),
				gas.WithSingletonService[gas.MigrationManager](migrate.New()),
				gas.WithSingletonService[*apikey.Service](apikey.New()),
			)
			t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
			err := w.Start(t.Context())
			if err == nil || !strings.HasPrefix(err.Error(), "gas: migrations:") || !strings.Contains(err.Error(), "BLOB, TEXT") {
				t.Errorf("Start with apikey on MySQL = %v, want a migrations failure from the TEXT default", err)
			}
		})
	}
}

// runMigrations builds and starts one store on db and returns the error from
// applying its migrations.
func runMigrations(t *testing.T, kind string, db gas.DatabaseProvider) error {
	t.Helper()
	mig := newMigrations(t, db)
	var (
		svc gas.Service
		err error
	)
	switch kind {
	case "session":
		svc, err = session.New(session.WithConfig(session.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	case "apikey":
		svc, err = apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	case "token":
		svc, err = token.New(token.WithConfig(token.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	}
	if err != nil {
		t.Fatalf("%s.New: %v", kind, err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("%s Init: %v", kind, err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return mig.RunPending()
}
