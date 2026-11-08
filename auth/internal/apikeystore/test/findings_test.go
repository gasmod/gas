package apikeystore_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/testutil"
)

// mysqlNoDefaultColumn matches a column definition whose type MySQL
// refuses a literal default on: TEXT, BLOB, JSON or GEOMETRY, in any size.
var mysqlNoDefaultColumn = regexp.MustCompile(
	`(?i)^\s*\w+\s+(TINY|MEDIUM|LONG)?(TEXT|BLOB)\b|^\s*\w+\s+(JSON|GEOMETRY)\b`,
)

// KI-044: the MySQL migration `create api keys` gave the TEXT column
// `scopes` a literal default, the empty string, which MySQL 8 refuses with
// error 1101, so the migration always failed there. No MySQL migration of the
// store now gives such a column a default.
//
// This checks the files on every run; TestKI044_MySQLAppliesMigrations
// checks that MySQL applies them, when the MySQL backend is on.
func TestKI044_MySQLMigrationsNoTextDefault(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("../mysql/*.up.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	checked := 0

	for _, file := range files {
		data, err := os.ReadFile(file) //nolint:gosec // G304: the path comes from a fixed glob.
		require.NoError(t, err)

		for line := range strings.Lines(string(data)) {
			if !mysqlNoDefaultColumn.MatchString(line) {
				continue
			}

			checked++

			assert.NotContains(t, strings.ToUpper(line), "DEFAULT", "%s: %s", file, line)
		}
	}

	assert.Positive(t, checked, "the scopes and metadata columns are checked")
}

// KI-044: on a MySQL server, the old `scopes TEXT NOT NULL DEFAULT ”`
// definition fails with error 1101, while the store's migrations apply
// and a key with scopes stores and reads back. It runs only with the
// MySQL backend on, as described in the testutil package documentation.
func TestKI044_MySQLAppliesMigrations(t *testing.T) {
	t.Parallel()

	b := testutil.MySQLBackend(t)
	ctx := t.Context()

	_, err := b.DB.DB().ExecContext(ctx,
		"CREATE TABLE ki044_old_scopes (scopes TEXT NOT NULL DEFAULT '')")

	var myErr *mysql.MySQLError

	require.ErrorAs(t, err, &myErr, "the old definition fails")
	assert.Equal(t, uint16(1101), myErr.Number)

	store := newStore(t, b)

	require.NoError(t, store.InsertKey(ctx, key("k1", "alice", "hash-1", time.Now())))

	got, err := store.GetKeyByHash(ctx, "hash-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"read", "write"}, got.Scopes)
}
