package db_test

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/db"
)

// mib is a mebibyte.
const mib = 1 << 20

// Names are exact on every dialect: case, accents and trailing spaces
// make other names, where MySQL's default collation made `Home.html`
// overwrite `home.html` (survey D2).
func TestStore_ExactNames(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			s := newEnv(t, b).store(t)
			ctx := t.Context()

			for _, n := range []string{"home.html", "Home.html", "café.html", "cafe.html"} {
				require.NoError(t, s.Register(ctx, n, []byte(n)))
			}

			for _, n := range []string{"home.html", "Home.html", "café.html", "cafe.html"} {
				got, err := s.Get(ctx, n)
				require.NoError(t, err)
				assert.Equal(t, n, string(got))
			}

			_, err := s.Get(ctx, "HOME.html")
			require.ErrorIs(t, err, template.ErrTemplateNotFound)

			names, err := s.List(ctx)
			require.NoError(t, err)
			assert.Equal(t, []string{"Home.html", "cafe.html", "café.html", "home.html"}, names)
		})
	}
}

// Content larger than MySQL's old 16 MiB MEDIUMBLOB reads back whole on
// every dialect, and any bytes are kept as they are.
func TestStore_LargeAndBinaryContent(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			s := newEnv(t, b).store(t, db.WithMaxSize(32*mib))
			ctx := t.Context()

			big := bytes.Repeat([]byte{0, 0xff, 'a'}, 17*mib/3+1)
			require.NoError(t, s.Register(ctx, "big.html", big))

			got, err := s.Get(ctx, "big.html")
			require.NoError(t, err)
			assert.True(t, bytes.Equal(big, got))
		})
	}
}

// On every dialect a name and a namespace are at most 255 characters,
// checked before any statement runs. 255 four-byte characters of each fit
// MySQL's columns and PostgreSQL's unique index, whose B-tree rows are at
// most 2704 bytes; before, PostgreSQL and SQLite took any length, and
// PostgreSQL failed on a long name only by its index.
func TestStore_NameLength(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			s := e.store(t, db.WithNamespace(strings.Repeat("\U0001F600", 255)))
			ctx := t.Context()
			longest := strings.Repeat("\U0001F600", 250) + ".html"
			tooLong := strings.Repeat("é", 251) + ".html"

			require.NoError(t, s.Register(ctx, longest, []byte("x")))

			got, err := s.Get(ctx, longest)
			require.NoError(t, err)
			assert.Equal(t, "x", string(got))

			err = s.Register(ctx, tooLong, nil)
			require.ErrorIs(t, err, template.ErrInvalidName)
			require.EqualError(t, err, "template: invalid name: longer than 255 characters")
		})
	}
}

// The MySQL upsert raises no warning: the old one used the VALUES()
// function, which MySQL 8.4 warns is deprecated (survey D6). One
// connection makes SHOW WARNINGS read the session of the upsert.
func TestStore_MySQLUpsertNoWarning(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend, func(s *database.Settings) {
		s.MaxOpenConns = 1
		s.MaxIdleConns = 1
	})
	s := e.store(t)
	ctx := t.Context()

	for range 2 {
		require.NoError(t, s.Register(ctx, "a.html", []byte("x")))
		assert.Equal(t, "0", e.queryString(t, "SELECT @@warning_count"))
	}
}

// columnInfo returns a column's data type and collation, or `none`, on
// e's MySQL database.
func columnInfo(t *testing.T, e *env, column string) (dataType, collation string) {
	t.Helper()

	require.NoError(t, e.db.DB().QueryRowContext(t.Context(),
		"SELECT DATA_TYPE, COALESCE(COLLATION_NAME, 'none') FROM information_schema.COLUMNS"+
			" WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		db.TableName, column).Scan(&dataType, &collation))

	return dataType, collation
}

// indexes returns the names of the indexes of the table on e's database.
func indexes(t *testing.T, e *env) []string {
	t.Helper()

	q := map[string]string{
		"mysql": "SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS" +
			" WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '" + db.TableName + "' ORDER BY INDEX_NAME",
		"postgres": "SELECT indexname FROM pg_indexes WHERE tablename = '" + db.TableName + "' ORDER BY indexname",
		"sqlite": "SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = '" + db.TableName +
			"' ORDER BY name",
	}[e.b.name]

	rows, err := e.db.DB().QueryContext(t.Context(), q)
	require.NoError(t, err)

	defer func() { require.NoError(t, rows.Close()) }()

	var out []string

	for rows.Next() {
		var n string

		require.NoError(t, rows.Scan(&n))

		out = append(out, n)
	}

	require.NoError(t, rows.Err())

	return out
}

// legacy applies only the first migration, as an earlier release did,
// with its own migrate service, owned by the store's name.
func legacy(t *testing.T, e *env) {
	t.Helper()

	m := startMigrate(t, e.db)
	m.Register(owner(db.ServiceName), gas.Migration{
		Version:     "20260322235959",
		Description: "create templates table",
		Up:          readMigration(t, e.b.name, "20260322235959_create_templates.up.sql"),
		Down:        readMigration(t, e.b.name, "20260322235959_create_templates.down.sql"),
	})
	require.NoError(t, m.RunPendingContext(t.Context()))
}

// A table created by the earlier release, with rows in it, is upgraded by
// the new migration alone: the rows stay, names become exact, the
// redundant index on namespace goes, and on MySQL content becomes
// LONGBLOB.
//
//nolint:funlen // one step of the upgrade after the other.
func TestMigration_UpgradeKeepsRows(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			legacy(t, e)

			insert := "INSERT INTO " + db.TableName + " (namespace, name, content) VALUES (?, ?, ?)"
			if b.name == "postgres" {
				insert = "INSERT INTO " + db.TableName + " (namespace, name, content) VALUES ($1, $2, $3)"
			}

			e.exec(t, insert, "default", "home.html", []byte("lower"))
			e.exec(t, insert, "default", "café.html", []byte("accent"))
			e.exec(t, insert, "emails", "a.html", []byte("mail"))
			assert.Contains(t, indexes(t, e), "idx___gas_templates_namespace")

			if b.name == "mysql" {
				typ, coll := columnInfo(t, e, "name")
				assert.Equal(t, "varchar/utf8mb4_0900_ai_ci", typ+"/"+coll)
			}

			s := e.store(t)
			ctx := t.Context()

			got, err := s.Get(ctx, "home.html")
			require.NoError(t, err)
			assert.Equal(t, "lower", string(got))

			got, err = s.Get(ctx, "café.html")
			require.NoError(t, err)
			assert.Equal(t, "accent", string(got))

			for _, n := range []string{"HOME.html", "cafe.html"} {
				_, err = s.Get(ctx, n)
				require.ErrorIs(t, err, template.ErrTemplateNotFound, n)
			}

			require.NoError(t, s.Register(ctx, "Home.html", []byte("upper")))

			got, err = s.Get(ctx, "home.html")
			require.NoError(t, err)
			assert.Equal(t, "lower", string(got))
			assert.NotContains(t, indexes(t, e), "idx___gas_templates_namespace")

			applied, err := e.migrations.Applied(ctx)
			require.NoError(t, err)
			require.Len(t, applied, 2)
			assert.Equal(t, "20261031120000", applied[1].Version)

			if b.name != "mysql" {
				return
			}

			for column, want := range map[string]string{
				"namespace": "varchar/utf8mb4_0900_bin",
				"name":      "varchar/utf8mb4_0900_bin",
				"content":   "longblob/none",
			} {
				typ, coll := columnInfo(t, e, column)
				assert.Equal(t, want, typ+"/"+coll, column)
			}
		})
	}
}

// Reverting the new migration restores the index, and on MySQL the
// default collation and MEDIUMBLOB, when no row is in the way.
func TestMigration_Down(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			s := e.store(t)
			require.NoError(t, s.Register(t.Context(), "a.html", []byte("a")))

			require.NoError(t, e.migrations.DownContext(t.Context(), 1))
			assert.Contains(t, indexes(t, e), "idx___gas_templates_namespace")

			if b.name == "mysql" {
				typ, coll := columnInfo(t, e, "name")
				assert.Equal(t, "varchar/utf8mb4_0900_ai_ci", typ+"/"+coll)

				typ, _ = columnInfo(t, e, "content")
				assert.Equal(t, "mediumblob", typ)
			}

			got, err := s.Get(t.Context(), "a.html")
			require.NoError(t, err)
			assert.Equal(t, "a", string(got))

			require.NoError(t, e.migrations.RunPendingContext(t.Context()))
			assert.NotContains(t, indexes(t, e), "idx___gas_templates_namespace")
		})
	}
}

// On MySQL, reverting fails, leaves the migration dirty and the table as
// it was, when two names differ only in case, content is over 16 MiB, or
// a name does not fit the database's character set. The migration sets
// strict mode on its session, so this holds on a server whose sql_mode is
// empty too, as on a latin1 database here; before, the revert cut such
// content to 16 MiB and turned such a name into `??.html`, and succeeded.
func TestMigration_MySQLDownBlocked(t *testing.T) {
	t.Parallel()

	big := bytes.Repeat([]byte("x"), 17*mib)

	for name, tc := range map[string]struct {
		rows      map[string][]byte
		nonStrict bool
	}{
		"case":            {rows: map[string][]byte{"home.html": nil, "Home.html": nil}},
		"large":           {rows: map[string][]byte{"big.html": big}},
		"large nonstrict": {rows: map[string][]byte{"big.html": big}, nonStrict: true},
		"name nonstrict":  {rows: map[string][]byte{"\u65e5\u672c.html": []byte("jp")}, nonStrict: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			b := mysqlBackend
			if tc.nonStrict {
				b.fresh = mysqlLatin1DSN
			}

			e := newEnv(t, b)
			s := e.store(t, db.WithMaxSize(32*mib))

			if tc.nonStrict {
				require.Empty(t, e.queryString(t, "SELECT @@SESSION.sql_mode"))
			}

			for n, content := range tc.rows {
				require.NoError(t, s.Register(t.Context(), n, content))
			}

			err := e.migrations.DownContext(t.Context(), 1)
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)

			applied, err := e.migrations.Applied(context.Background())
			require.NoError(t, err)
			assert.True(t, applied[1].Dirty)

			typ, coll := columnInfo(t, e, "name")
			assert.Equal(t, "varchar/utf8mb4_0900_bin", typ+"/"+coll)

			typ, _ = columnInfo(t, e, "content")
			assert.Equal(t, "longblob", typ)

			for n, content := range tc.rows {
				got, err := s.Get(t.Context(), n)
				require.NoError(t, err, n)
				assert.Len(t, got, len(content), n)
			}
		})
	}
}

// mysqlLatin1DSN returns the DSN of a new latin1 database on the shared
// MySQL container whose sessions start with an empty sql_mode, so that
// nothing is strict unless a statement makes it so.
func mysqlLatin1DSN(t *testing.T) string {
	t.Helper()

	dsn := mysqlDSN(t)

	cfg, err := mysql.ParseDSN(dsn)
	require.NoError(t, err)

	conn, err := sql.Open("mysql", dsn)
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	_, err = conn.ExecContext(t.Context(),
		"ALTER DATABASE "+cfg.DBName+" CHARACTER SET latin1 COLLATE latin1_swedish_ci")
	require.NoError(t, err)

	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}

	cfg.Params["sql_mode"] = "''"

	return cfg.FormatDSN()
}
