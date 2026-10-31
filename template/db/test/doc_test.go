package db_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/db"
	"github.com/gasmod/gas/template/internal/conformance"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	e := newEnv(t, sqliteBackend)
	database, logger, migrations := e.db, &gas.NopLogger{}, e.migrations

	store, err := db.NewStore()(database, logger, migrations)
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	require.NoError(t, migrations.RunPendingContext(ctx))

	err = store.Register(ctx, "welcome.html", []byte("<p>Hi {{.Name}}</p>"))
	require.NoError(t, err)

	assert.Equal(t, "default", e.queryString(t, "SELECT namespace FROM __gas_templates"))
	assert.Equal(t, "welcome.html", e.queryString(t, "SELECT name FROM __gas_templates"))
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)
	log := &gas.NopLogger{}
	withNamespace := func(ns string) error {
		_, err := db.NewStore(db.WithNamespace(ns))(e.db, log, e.migrations)

		return err
	}
	// gone returns a store whose table is dropped.
	gone := func() *db.Store {
		g := newEnv(t, sqliteBackend)
		s := g.store(t)
		g.exec(t, "DROP TABLE "+db.TableName)

		return s
	}
	ctx := context.Background()

	cases := map[string]func() error{
		"template: database is nil": func() error {
			_, err := db.NewStore()(nil, log, e.migrations)

			return err
		},
		"template: logger is nil": func() error {
			_, err := db.NewStore()(e.db, nil, e.migrations)

			return err
		},
		"template: migration manager is nil": func() error {
			_, err := db.NewStore()(e.db, log, nil)

			return err
		},
		"template: invalid namespace: empty":            func() error { return withNamespace("") },
		"template: invalid namespace: not valid UTF-8":  func() error { return withNamespace("\xff") },
		"template: invalid namespace: holds a NUL byte": func() error { return withNamespace("\x00") },
		"template: invalid namespace: longer than 255 characters": func() error {
			return withNamespace(strings.Repeat("n", 256))
		},
		"template: unsupported driver: supported are postgres, mysql and sqlite": func() error {
			s, err := db.NewStore()(
				driverOverride{Service: e.db, driver: "oracle"},
				log,
				e.migrations,
			)
			require.NoError(t, err)

			return s.Init(ctx)
		},
		"template: invalid name: longer than 255 characters": func() error {
			return tooLong(t)
		},
		`template: query failed: get "welcome.html": sqlite error 1`: func() error {
			_, err := gone().Get(ctx, "welcome.html")

			return err
		},
		`template: query failed: get "welcome.html": context canceled`: func() error {
			canceled, cancel := context.WithCancel(ctx)
			cancel()

			_, err := gone().Get(canceled, "welcome.html")

			return err
		},
		"template: query failed: list: sqlite error 1": func() error {
			_, err := gone().List(ctx)

			return err
		},
		`template: query failed: register "welcome.html": sqlite error 1`: func() error {
			return gone().Register(ctx, "welcome.html", nil)
		},
		`template: query failed: exists "welcome.html": sqlite error 1`: func() error {
			_, err := gone().Exists(ctx, "welcome.html")

			return err
		},
		`template: query failed: delete "welcome.html": sqlite error 1`: func() error {
			return gone().Delete(ctx, "welcome.html")
		},
	}

	documented := conformance.DocLines(t, "../doc.go", "template: ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}

// tooLong returns the error of registering a name of 256 characters,
// which the store refuses before any statement runs.
func tooLong(t *testing.T) error {
	t.Helper()

	s := newEnv(t, sqliteBackend).store(t)

	return s.RegisterFS(t.Context(), fstest.MapFS{strings.Repeat("n", 251) + ".html": {}})
}
