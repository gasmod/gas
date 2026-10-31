package database_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
)

// DefaultConfig holds the documented defaults: ModeSQL, the Default
// limits and timeouts, and no dialect or DSN.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &database.Config{Database: database.Settings{
		Mode:              database.ModeSQL,
		MaxOpenConns:      25,
		MaxIdleConns:      5,
		ConnMaxLifetime:   30 * time.Minute,
		ConnMaxIdleTime:   5 * time.Minute,
		ConnectTimeout:    5 * time.Second,
		ConnRetries:       0,
		ConnRetryInterval: 2 * time.Second,
	}}, database.DefaultConfig())
	assert.Equal(t, 30*time.Second, database.MaxConnRetryInterval)
	assert.Equal(t, "gas/database", database.ServiceName)
}

// configCase is a configuration that Init accepts or rejects, the same way
// whether it is given to WithConfig or bound from a ConfigProvider.
type configCase struct {
	// edit changes a valid SQLite configuration.
	edit func(*database.Settings)
	// connector, when set, is given to WithConnector with dialect.
	connector driver.Connector
	// name names the case.
	name string
	// dialect is the dialect given with connector.
	dialect database.Dialect
	// want is the error Init returns, or empty when Init succeeds.
	want string
	// tag reports whether want is a failed validate tag.
	tag bool
	// nilConnector reports whether WithConnector is given a nil connector.
	nilConnector bool
}

// tagErr returns the error Init returns for field of Settings failing tag.
func tagErr(field, tag string) string {
	return "gas/database: invalid config: validate config: Key: 'Config.Database." + field +
		"' Error:Field validation for '" + field + "' failed on the '" + tag + "' tag"
}

// ruleErr returns the error Init returns for a broken rule.
func ruleErr(msg string) string {
	return "gas/database: invalid config: " + msg
}

// configCases are the tags and the rules of the package documentation,
// each at a bound it accepts and one it rejects.
//
//nolint:funlen // one case per rule and bound.
func configCases() []configCase {
	sqlite := func(*database.Settings) {}
	f := &fake{}

	return []configCase{
		{name: "valid", edit: sqlite},
		{
			name: "mode empty",
			edit: func(s *database.Settings) { s.Mode = "" },
			want: tagErr("Mode", "oneof"),
			tag:  true,
		},
		{
			name: "mode unknown",
			edit: func(s *database.Settings) { s.Mode = "mongo" },
			want: tagErr("Mode", "oneof"),
			tag:  true,
		},
		{
			name: "dialect unknown",
			edit: func(s *database.Settings) { s.Dialect = "oracle" },
			want: tagErr("Dialect", "oneof"),
			tag:  true,
		},
		{
			name: "dialect empty",
			edit: func(s *database.Settings) { s.Dialect = "" },
			want: ruleErr("Database.Dialect is required without a connector"),
		},
		{
			name: "dsn empty",
			edit: func(s *database.Settings) { s.DSN = "" },
			want: ruleErr("Database.DSN is required without a connector"),
		},
		{
			name: "pgx with sqlite",
			edit: func(s *database.Settings) { s.Mode = database.ModePgx },
			want: ruleErr("Database.Mode pgx requires Database.Dialect postgres"),
		},
		{
			name: "open conns 1",
			edit: func(s *database.Settings) { s.MaxOpenConns, s.MaxIdleConns = 1, 1 },
		},
		{name: "open conns 1000", edit: func(s *database.Settings) { s.MaxOpenConns = 1000 }},
		{
			name: "open conns 0",
			edit: func(s *database.Settings) { s.MaxOpenConns, s.MaxIdleConns = 0, 0 },
			want: tagErr("MaxOpenConns", "min"),
			tag:  true,
		},
		{
			name: "open conns 1001",
			edit: func(s *database.Settings) { s.MaxOpenConns = 1001 },
			want: tagErr("MaxOpenConns", "max"),
			tag:  true,
		},
		{name: "idle conns 0", edit: func(s *database.Settings) { s.MaxIdleConns = 0 }},
		{name: "idle conns at open", edit: func(s *database.Settings) { s.MaxIdleConns = 25 }},
		{
			name: "idle conns negative",
			edit: func(s *database.Settings) { s.MaxIdleConns = -1 },
			want: tagErr("MaxIdleConns", "min"),
			tag:  true,
		},
		{
			name: "idle conns above open",
			edit: func(s *database.Settings) { s.MaxIdleConns = 26 },
			want: ruleErr("Database.MaxIdleConns must not exceed Database.MaxOpenConns"),
		},
		{
			name: "lifetime 1s",
			edit: func(s *database.Settings) { s.ConnMaxLifetime, s.ConnMaxIdleTime = time.Second, time.Second },
		},
		{
			name: "lifetime 2h",
			edit: func(s *database.Settings) { s.ConnMaxLifetime = 2 * time.Hour },
		},
		{
			name: "lifetime under 1s",
			edit: func(s *database.Settings) { s.ConnMaxLifetime = time.Second - 1 },
			// The idle time, at least 1s, is then over the lifetime too.
			want: tagErr("ConnMaxLifetime", "min") + "\n" +
				strings.TrimPrefix(
					tagErr("ConnMaxIdleTime", "ltefield"),
					"gas/database: invalid config: validate config: ",
				),
			tag: true,
		},
		{
			name: "lifetime over 2h",
			edit: func(s *database.Settings) { s.ConnMaxLifetime = 2*time.Hour + 1 },
			want: tagErr("ConnMaxLifetime", "max"),
			tag:  true,
		},
		{
			name: "idle time 1h",
			edit: func(s *database.Settings) { s.ConnMaxLifetime, s.ConnMaxIdleTime = 2*time.Hour, time.Hour },
		},
		{
			name: "idle time under 1s",
			edit: func(s *database.Settings) { s.ConnMaxIdleTime = time.Second - 1 },
			want: tagErr("ConnMaxIdleTime", "min"),
			tag:  true,
		},
		{
			name: "idle time over 1h",
			edit: func(s *database.Settings) { s.ConnMaxLifetime, s.ConnMaxIdleTime = 2*time.Hour, time.Hour+1 },
			want: tagErr("ConnMaxIdleTime", "max"),
			tag:  true,
		},
		{
			name: "idle time over lifetime",
			edit: func(s *database.Settings) { s.ConnMaxIdleTime = s.ConnMaxLifetime + time.Second },
			want: tagErr("ConnMaxIdleTime", "ltefield"),
			tag:  true,
		},
		{
			name: "connect timeout 1s",
			edit: func(s *database.Settings) { s.ConnectTimeout = time.Second },
		},
		{
			name: "connect timeout 1m",
			edit: func(s *database.Settings) { s.ConnectTimeout = time.Minute },
		},
		{
			name: "connect timeout under 1s",
			edit: func(s *database.Settings) { s.ConnectTimeout = time.Second - 1 },
			want: tagErr("ConnectTimeout", "min"),
			tag:  true,
		},
		{
			name: "connect timeout over 1m",
			edit: func(s *database.Settings) { s.ConnectTimeout = time.Minute + 1 },
			want: tagErr("ConnectTimeout", "max"),
			tag:  true,
		},
		{name: "retries 10", edit: func(s *database.Settings) { s.ConnRetries = 10 }},
		{
			name: "retries negative",
			edit: func(s *database.Settings) { s.ConnRetries = -1 },
			want: tagErr("ConnRetries", "min"),
			tag:  true,
		},
		{
			name: "retries 11",
			edit: func(s *database.Settings) { s.ConnRetries = 11 },
			want: tagErr("ConnRetries", "max"),
			tag:  true,
		},
		{
			name: "interval unchecked without retries",
			edit: func(s *database.Settings) { s.ConnRetryInterval = 0 },
		},
		{
			name: "interval 100ms",
			edit: func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 100*time.Millisecond },
		},
		{
			name: "interval 30s",
			edit: func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 30*time.Second },
		},
		{
			name: "interval MaxConnRetryInterval",
			edit: func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 10, database.MaxConnRetryInterval },
		},
		{
			name: "interval under 100ms",
			edit: func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 100*time.Millisecond-1 },
			want: ruleErr(
				"Database.ConnRetryInterval must be 100ms to 30s when Database.ConnRetries is positive",
			),
		},
		{
			name: "interval over 30s",
			edit: func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, 30*time.Second+1 },
			want: ruleErr(
				"Database.ConnRetryInterval must be 100ms to 30s when Database.ConnRetries is positive",
			),
		},
		{
			name: "interval 1m",
			edit: func(s *database.Settings) { s.ConnRetries, s.ConnRetryInterval = 1, time.Minute },
			want: ruleErr(
				"Database.ConnRetryInterval must be 100ms to 30s when Database.ConnRetries is positive",
			),
		},
		{
			name:      "connector",
			edit:      func(s *database.Settings) { s.Dialect, s.DSN = "", "" },
			connector: f.connector(),
			dialect:   database.DialectMySQL,
		},
		{
			name:      "connector with its dialect",
			edit:      func(s *database.Settings) { s.Dialect, s.DSN = database.DialectMySQL, "" },
			connector: f.connector(),
			dialect:   database.DialectMySQL,
		},
		{
			name:         "connector nil",
			edit:         func(s *database.Settings) { s.Dialect, s.DSN = "", "" },
			nilConnector: true,
			dialect:      database.DialectMySQL,
			want:         ruleErr("WithConnector requires a connector"),
		},
		{
			name:      "connector typed nil",
			edit:      func(s *database.Settings) { s.Dialect, s.DSN = "", "" },
			connector: (*fakeConnector)(nil),
			dialect:   database.DialectMySQL,
			want:      ruleErr("WithConnector requires a connector"),
		},
		{
			name:      "connector without dialect",
			edit:      func(s *database.Settings) { s.Dialect, s.DSN = "", "" },
			connector: f.connector(),
			dialect:   "",
			want:      ruleErr("WithConnector requires the dialect postgres, mysql or sqlite"),
		},
		{
			name:      "connector in pgx mode",
			edit:      func(s *database.Settings) { s.Mode, s.Dialect, s.DSN = database.ModePgx, "", "" },
			connector: f.connector(),
			dialect:   database.DialectPostgres,
			want:      ruleErr("a connector requires Database.Mode sql"),
		},
		{
			name:      "connector with another dialect",
			edit:      func(s *database.Settings) { s.DSN = "" },
			connector: f.connector(),
			dialect:   database.DialectMySQL,
			want: ruleErr(
				"with a connector, Database.Dialect must be empty or the connector's dialect",
			),
		},
		{
			name:      "connector with a DSN",
			edit:      func(s *database.Settings) { s.Dialect = "" },
			connector: f.connector(),
			dialect:   database.DialectSQLite,
			want:      ruleErr("with a connector, Database.DSN must be empty"),
		},
	}
}

// options returns the options of c for a configuration given or bound.
func (c configCase) options(given *database.Config) []database.Option {
	var opts []database.Option

	if given != nil {
		opts = append(opts, database.WithConfig(given))
	}

	if c.connector != nil || c.nilConnector {
		opts = append(opts, database.WithConnector(c.dialect, c.connector))
	}

	return opts
}

// Every tag and rule holds the same way for a configuration given to
// WithConfig and for one bound from the ConfigProvider, with the same
// error, which names the setting and wraps ErrInvalidConfig.
func TestInit_ConfigRules(t *testing.T) {
	t.Parallel()

	for _, c := range configCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cfg := withSettings(t, c.edit)

			paths := map[string]func() (*database.Service, error){
				"given": func() (*database.Service, error) {
					return database.New(c.options(cfg)...)(nil, &gas.NopLogger{})
				},
				"bound": func() (*database.Service, error) {
					return database.New(
						c.options(nil)...)(
						provider(t, &cfg.Database),
						&gas.NopLogger{},
					)
				},
			}

			for path, newService := range paths {
				s, err := newService()
				require.NoError(t, err)

				err = s.Init(t.Context())
				if c.want == "" {
					require.NoError(t, err, path)
					require.NoError(t, s.Close(context.Background()), path)

					continue
				}

				require.ErrorIs(t, err, database.ErrInvalidConfig, path)
				require.EqualError(t, err, c.want, path)

				if c.tag {
					var verrs validator.ValidationErrors

					require.ErrorAs(t, err, &verrs, path)
				}

				assert.Nil(t, s.DB(), path)
			}
		})
	}
}

// Init copies the configuration given to WithConfig: it never changes the
// caller's struct, and changes the caller makes after Init do not reach
// the service until it is initialized again.
func TestWithConfig_Copies(t *testing.T) {
	t.Parallel()

	cfg := sqliteConfig(t)
	before := *cfg

	s := initialized(t, database.WithConfig(cfg))
	assert.Equal(t, before, *cfg)

	cfg.Database.Dialect = "oracle"

	assert.Equal(t, "sqlite", s.Driver())
	require.NoError(t, s.Close(t.Context()))

	err := s.Init(t.Context())
	require.EqualError(t, err, tagErr("Dialect", "oneof"))
}

// A nil configuration given to WithConfig fails Init with ErrNilConfig.
func TestWithConfig_Nil(t *testing.T) {
	t.Parallel()

	err := initErr(t, database.WithConfig(nil))
	require.ErrorIs(t, err, database.ErrNilConfig)
	require.EqualError(t, err, "gas/database: config is nil")
}

// Without WithConfig, Init binds DefaultConfig from the ConfigProvider, so
// a key the configuration does not set keeps its default.
func TestInit_BindsOverDefaults(t *testing.T) {
	t.Parallel()

	dsn := sqliteConfig(t).Database.DSN

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"database": map[string]any{
			"dialect":      "sqlite",
			"dsn":          string(dsn),
			"maxopenconns": 3,
			"maxidleconns": 2,
		},
	})
	require.NoError(t, err)

	s, err := database.New()(p, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	stats := s.DB().Stats()
	assert.Equal(t, 3, stats.MaxOpenConnections)
	assert.Equal(t, "sqlite", s.Driver())
}

// A ConfigProvider that fails to bind fails Init with ErrInvalidConfig,
// wrapping the provider's error.
func TestInit_BindError(t *testing.T) {
	t.Parallel()

	p := &configtest.MockConfig{BindFn: func(any) error { return errFake }}

	s, err := database.New()(p, &gas.NopLogger{})
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, database.ErrInvalidConfig)
	require.ErrorIs(t, err, errFake)
	require.EqualError(t, err, "gas/database: invalid config: fake: not supported")
}

// Without WithConfig and without a ConfigProvider, Init fails with
// ErrNoConfigProvider.
func TestInit_NoConfigProvider(t *testing.T) {
	t.Parallel()

	err := initErr(t)
	require.ErrorIs(t, err, database.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/database: no config provider")
}

// New's constructor fails with ErrNilLogger for a nil logger, rather than
// panicking.
func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	s, err := database.New()(nil, nil)
	require.ErrorIs(t, err, database.ErrNilLogger)
	require.EqualError(t, err, "gas/database: logger is nil")
	assert.Nil(t, s)
}

// New returns the constructor in the shape the gas container expects.
func TestNew_Shape(t *testing.T) {
	t.Parallel()

	ctor := database.New()

	s, err := ctor(nil, &gas.NopLogger{})
	require.NoError(t, err)
	assert.Equal(t, database.ServiceName, s.Name())
}

// The DSN printed raw with fmt, encoding/json, a YAML encoder and
// log/slog. It is now a gas.Secret, which each writes as `[redacted]`,
// and binds from `database.dsn` as a string: the service opens it.
func TestConfig_DSNRedacted(t *testing.T) {
	t.Parallel()

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectPostgres
	cfg.Database.DSN = "postgres://app:zqxsecret@db.internal:5432/app"

	j, err := json.Marshal(cfg) //nolint:musttag // untagged: the field names an encoder writes.
	require.NoError(t, err)

	y, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var logs bytes.Buffer

	slog.New(slog.NewTextHandler(&logs, nil)).Info("cfg", "cfg", cfg, "dsn", cfg.Database.DSN)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("cfg", "cfg", cfg, "dsn", cfg.Database.DSN)

	for _, text := range []string{
		fmt.Sprintf("%v %+v %#v %s %q %x", cfg, cfg, cfg, cfg.Database.DSN, cfg.Database.DSN, cfg.Database.DSN),
		string(j),
		string(y),
		logs.String(),
	} {
		assert.NotContains(t, text, "zqxsecret")
		assert.Contains(t, text, "[redacted]")
	}

	dsn := string(sqliteConfig(t).Database.DSN)

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"database": map[string]any{"dialect": "sqlite", "dsn": dsn},
	})
	require.NoError(t, err)

	s, err := database.New()(p, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	require.NoError(t, s.DB().PingContext(t.Context()))
}
