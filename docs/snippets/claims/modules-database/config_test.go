package modulesdatabase_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas/database"
)

// Docs: modules/database.mdx#what-it-is-for
// "| `ModeSQL` (`sql`, the default) |" and "| `ModePgx` (`pgx`) |"
func TestModeNamesAndDefault(t *testing.T) {
	t.Parallel()
	if database.ModeSQL != "sql" || database.ModePgx != "pgx" {
		t.Errorf("ModeSQL, ModePgx = %q, %q; want sql, pgx", database.ModeSQL, database.ModePgx)
	}
	if got := database.DefaultConfig().Database.Mode; got != database.ModeSQL {
		t.Errorf("DefaultConfig().Database.Mode = %q, want %q", got, database.ModeSQL)
	}
}

// Docs: modules/database.mdx#configuration
// "| `database.mode` | string | `sql` |" and "| `database.driver` | string | `postgres` |"
// and "| `database.dsn` | string | |" and "| `database.maxopenconns` | int32 | `25` |"
// and "| `database.maxidleconns` | int | `5` |" and "| `database.connmaxlifetime` | duration | `30m` |"
// and "| `database.connmaxidletime` | duration | `5m` |" and "| `database.connretries` | int | `0` |"
// and "| `database.connretryinterval` | duration | `2s` |"
func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	got := database.DefaultConfig().Database
	want := database.Settings{
		Mode:              "sql",
		Driver:            "postgres",
		DSN:               "",
		MaxOpenConns:      25,
		MaxIdleConns:      5,
		ConnMaxLifetime:   30 * time.Minute,
		ConnMaxIdleTime:   5 * time.Minute,
		ConnRetries:       0,
		ConnRetryInterval: 2 * time.Second,
	}
	if got != want {
		t.Fatalf("DefaultConfig().Database = %+v, want %+v", got, want)
	}
}

// Docs: modules/database.mdx#options
// "a bare `&database.Config{}` leaves `Mode` empty and every pool limit at zero."
func TestBareConfigIsEmpty(t *testing.T) {
	t.Parallel()
	s := (&database.Config{}).Database
	if s.Mode != "" || s.MaxOpenConns != 0 || s.MaxIdleConns != 0 || s.ConnMaxLifetime != 0 || s.ConnMaxIdleTime != 0 {
		t.Fatalf("&database.Config{} settings = %+v, want empty Mode and zero pool limits", s)
	}
}

// boundInit binds the database settings in doc from a JSON config and runs
// Init. A sqlite driver and a fresh DSN are added unless doc sets them.
func boundInit(t *testing.T, doc map[string]any) (*database.Service, error) {
	t.Helper()
	settings := map[string]any{"driver": "sqlite", "dsn": sqliteDSN(t)}
	for k, v := range doc {
		settings[k] = v
	}
	svc := build(t, jsonConfig(t, map[string]any{"database": settings}), nil)
	return svc, svc.Init(t.Context())
}

// Docs: modules/database.mdx#configuration
// "Unless `WithConfig` is given, `Init` binds the settings from the
// `gas.ConfigProvider` under the `database` key, over `DefaultConfig()`, so a
// key you do not set keeps its default."
func TestInitBindsOverDefaults(t *testing.T) {
	t.Parallel()
	svc, err := boundInit(t, map[string]any{"maxidleconns": 3})
	if err != nil {
		t.Fatalf("Init with bound settings: %v", err)
	}
	if got := svc.DB().Stats().MaxOpenConnections; got != 25 {
		t.Errorf("MaxOpenConnections with maxopenconns unset = %d, want the default 25", got)
	}
	if got := svc.Driver(); got != database.DriverSQLite {
		t.Errorf("Driver() = %q, want the bound sqlite", got)
	}
}

// Docs: modules/database.mdx#configuration
// "Unless `WithConfig` is given, `Init` binds the settings"
// and
// Docs: modules/database.mdx#wiring
// "The settings normally come from the `gas.ConfigProvider` under the
// `database` key" and "It replaces the whole configuration and skips binding
// from the `gas.ConfigProvider`."
func TestBindHappensInInitUnlessWithConfig(t *testing.T) {
	t.Parallel()

	t.Run("Bound", func(t *testing.T) {
		t.Parallel()
		cp := &countingConfig{ConfigProvider: jsonConfig(t, map[string]any{
			"database": map[string]any{"driver": "sqlite", "dsn": sqliteDSN(t)},
		})}
		svc := build(t, cp, nil)
		if n := cp.binds.Load(); n != 0 {
			t.Fatalf("Bind calls after construction = %d, want 0", n)
		}
		if err := svc.Init(t.Context()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if n := cp.binds.Load(); n != 1 {
			t.Fatalf("Bind calls after Init = %d, want 1", n)
		}
	})
	t.Run("WithConfig", func(t *testing.T) {
		t.Parallel()
		// The provider holds settings that would fail Init if they were bound.
		cp := &countingConfig{ConfigProvider: jsonConfig(t, map[string]any{
			"database": map[string]any{"driver": "nosuch", "dsn": "", "maxopenconns": 0},
		})}
		svc := build(t, cp, nil, database.WithConfig(sqliteConfig(t)))
		if err := svc.Init(t.Context()); err != nil {
			t.Fatalf("Init with WithConfig: %v", err)
		}
		if n := cp.binds.Load(); n != 0 {
			t.Fatalf("Bind calls with WithConfig = %d, want 0", n)
		}
	})
}

// Docs: modules/database.mdx#configuration
// "Keys are case-insensitive"
func TestKeysCaseInsensitive(t *testing.T) {
	t.Parallel()
	svc := build(t, jsonConfig(t, map[string]any{
		"DataBase": map[string]any{"DRIVER": "sqlite", "Dsn": sqliteDSN(t), "MaxOpenConns": 7},
	}), nil)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init with mixed-case keys: %v", err)
	}
	if got := svc.DB().Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections from MaxOpenConns = %d, want 7", got)
	}
}

// Docs: modules/database.mdx#configuration
// "| `database.mode` | string | `sql` | `sql` or `pgx` |"
// and "| `database.maxopenconns` | int32 | `25` | Maximum open connections, 1 to 1000 |"
// and "| `database.connmaxlifetime` | duration | `30m` | Maximum time a connection is reused, 1s to 2h |"
// and "| `database.connmaxidletime` | duration | `5m` | Maximum time a connection sits idle, 1s to 1h and at most `connmaxlifetime` |"
// and "The rules on `mode`, `maxopenconns`, `connmaxlifetime` and `connmaxidletime`
// are `validate` tags, checked when the config is bound"
// and
// Docs: modules/database.mdx#errors
// "| `gas/database: config binding: ...` | Binding or validating the bound config failed |"
func TestBoundTagRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		doc  map[string]any
		ok   bool
	}{
		{"ModeBogus", map[string]any{"mode": "bogus"}, false},
		{"MaxOpenConns0", map[string]any{"maxopenconns": 0, "maxidleconns": 1}, false},
		{"MaxOpenConns1", map[string]any{"maxopenconns": 1, "maxidleconns": 1}, true},
		{"MaxOpenConns1000", map[string]any{"maxopenconns": 1000}, true},
		{"MaxOpenConns1001", map[string]any{"maxopenconns": 1001}, false},
		{"Lifetime999ms", map[string]any{"connmaxlifetime": "999ms", "connmaxidletime": "999ms"}, false},
		{"Lifetime1s", map[string]any{"connmaxlifetime": "1s", "connmaxidletime": "1s"}, true},
		{"Lifetime2h", map[string]any{"connmaxlifetime": "2h"}, true},
		{"Lifetime2h1s", map[string]any{"connmaxlifetime": "2h1s"}, false},
		{"IdleTime999ms", map[string]any{"connmaxidletime": "999ms"}, false},
		{"IdleTime1h", map[string]any{"connmaxlifetime": "2h", "connmaxidletime": "1h"}, true},
		{"IdleTime1h1s", map[string]any{"connmaxlifetime": "2h", "connmaxidletime": "1h1s"}, false},
		{"IdleTimeOverLifetime", map[string]any{"connmaxlifetime": "10m", "connmaxidletime": "11m"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := boundInit(t, tc.doc)
			if tc.ok {
				if err != nil {
					t.Fatalf("Init with %v: %v, want nil", tc.doc, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Init with %v: err = nil, want a config binding error", tc.doc)
			}
			if !strings.HasPrefix(err.Error(), "gas/database: config binding: ") {
				t.Errorf("Init err = %q, want prefix %q", err, "gas/database: config binding: ")
			}
			var verrs validator.ValidationErrors
			if !errors.As(err, &verrs) {
				t.Errorf("Init err = %v, want it to wrap the validator's error", err)
			}
		})
	}
}

// Docs: modules/database.mdx#configuration
// "so a configuration given with `WithConfig` skips them."
// Mode is left out: with WithConfig an unknown mode still fails Init, by
// another route than the tag, so the tag being skipped is not observable.
func TestWithConfigSkipsTagRules(t *testing.T) {
	t.Parallel()
	cfg := sqliteConfig(t)
	cfg.Database.MaxOpenConns = 2000
	cfg.Database.ConnMaxLifetime = 3 * time.Hour
	cfg.Database.ConnMaxIdleTime = 2 * time.Hour
	svc := startService(t, cfg, nil)
	if got := svc.DB().Stats().MaxOpenConnections; got != 2000 {
		t.Fatalf("MaxOpenConnections = %d, want the unchecked 2000", got)
	}
}

// Docs: modules/database.mdx#configuration
// "The other rules, on the driver name, the DSN, the connector's mode, and (in
// sql mode) `maxidleconns`, are checked in `Init` for any configuration, bound
// or given. A rule that does not hold fails `Init`, and the app does not start."
// and "| `database.driver` | string | `postgres` | `database/sql` driver name: `postgres`, `pgx` or `sqlite`."
// and "| `database.dsn` | string | | Connection string. Required unless a connector is set |"
// and "| `database.maxidleconns` | int | `5` | Maximum idle connections, 1 to 1000 and at most `maxopenconns`."
// and
// Docs: modules/database.mdx#errors
// "| `gas/database: invalid configuration: ...` | A rule checked in `Init` failed; the message names the field, such as `Database.DSN` |"
func TestInitRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		edit  func(*database.Config)
		field string // empty when the configuration is valid
	}{
		{"DriverMySQL", func(c *database.Config) { c.Database.Driver = "mysql" }, "Database.Driver"},
		{"DSNEmpty", func(c *database.Config) { c.Database.DSN = "" }, "Database.DSN"},
		{"IdleConns0", func(c *database.Config) { c.Database.MaxIdleConns = 0 }, "Database.MaxIdleConns"},
		{"IdleConns1", func(c *database.Config) { c.Database.MaxIdleConns = 1 }, ""},
		{"IdleConns1000", func(c *database.Config) {
			c.Database.MaxOpenConns = 1000
			c.Database.MaxIdleConns = 1000
		}, ""},
		{"IdleConns1001", func(c *database.Config) {
			c.Database.MaxOpenConns = 1000
			c.Database.MaxIdleConns = 1001
		}, "Database.MaxIdleConns"},
		{"IdleOverOpen", func(c *database.Config) {
			c.Database.MaxOpenConns = 3
			c.Database.MaxIdleConns = 4
		}, "Database.MaxIdleConns"},
	} {
		for _, bound := range []bool{false, true} {
			name := tc.name + "/Given"
			if bound {
				name = tc.name + "/Bound"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				cfg := sqliteConfig(t)
				tc.edit(cfg)
				var err error
				if bound {
					s := cfg.Database
					_, err = boundInit(t, map[string]any{
						"driver": s.Driver, "dsn": s.DSN,
						"maxopenconns": s.MaxOpenConns, "maxidleconns": s.MaxIdleConns,
					})
				} else {
					err = build(t, nil, nil, database.WithConfig(cfg)).Init(t.Context())
				}
				if tc.field == "" {
					if err != nil {
						t.Fatalf("Init: %v, want nil", err)
					}
					return
				}
				if err == nil {
					t.Fatalf("Init: err = nil, want an invalid configuration error naming %s", tc.field)
				}
				msg := err.Error()
				if !strings.HasPrefix(msg, "gas/database: invalid configuration: ") || !strings.Contains(msg, tc.field) {
					t.Fatalf("Init err = %q, want prefix %q naming %s", msg, "gas/database: invalid configuration: ", tc.field)
				}
			})
		}
	}
}

// Docs: modules/database.mdx#configuration
// "| `database.driver` | string | `postgres` | `database/sql` driver name: `postgres`, `pgx` or `sqlite`. Sql mode only; not checked when a connector is set |"
// and "| `database.dsn` | string | | Connection string. Required unless a connector is set |"
// and
// Docs: modules/database.mdx#connectors
// "With a connector, `database.driver` and `database.dsn` are not required and
// the driver name is not checked."
func TestConnectorSkipsDriverAndDSN(t *testing.T) {
	t.Parallel()
	for name, driverName := range map[string]string{"Empty": "", "Unlisted": "mysql"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := connectorConfig()
			cfg.Database.Driver = driverName
			conn := &fakeConnector{}
			svc := startService(t, cfg, nil, database.WithConnector(conn))
			if err := svc.Ping(t.Context()); err != nil {
				t.Fatalf("Ping through the connector: %v", err)
			}
		})
	}
}
