package mysqlsplit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/migrate/internal/mysqlsplit"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	stmts := mysqlsplit.Split(
		"CREATE TABLE t (id INT); -- done\nINSERT INTO t VALUES (1);",
		mysqlsplit.Mode{},
	)

	assert.Equal(t, []string{"CREATE TABLE t (id INT)", "-- done\nINSERT INTO t VALUES (1)"}, stmts)
}

// Semicolons split statements only outside strings, quoted identifiers
// and comments, read as MySQL reads them.
func TestSplit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"white space", " \n\t;; \r\n", nil},
		{"one, no semicolon", "SELECT 1", []string{"SELECT 1"}},
		{"trimmed", "  SELECT 1 ;\n\n SELECT 2;  ", []string{"SELECT 1", "SELECT 2"}},
		{"single quotes", "SELECT 'a;b'; SELECT 2", []string{"SELECT 'a;b'", "SELECT 2"}},
		{"doubled quote", "SELECT 'it''s;'; SELECT 2", []string{"SELECT 'it''s;'", "SELECT 2"}},
		{"backslash", `SELECT 'a\';b'; SELECT 2`, []string{`SELECT 'a\';b'`, "SELECT 2"}},
		{"double quotes", `SELECT "a;\"b"; SELECT 2`, []string{`SELECT "a;\"b"`, "SELECT 2"}},
		{"backticks", "SELECT `a;``b\\`; SELECT 2", []string{"SELECT `a;``b\\`", "SELECT 2"}},
		{"dash comment", "SELECT 1 -- a;b\n; SELECT 2", []string{"SELECT 1 -- a;b", "SELECT 2"}},
		{"dash tab", "SELECT 1 --\ta;b\n; SELECT 2", []string{"SELECT 1 --\ta;b", "SELECT 2"}},
		{"dash at end", "SELECT 1; --", []string{"SELECT 1"}},
		{"dashes not a comment", "SELECT 1--1; SELECT 2", []string{"SELECT 1--1", "SELECT 2"}},
		{"hash comment", "SELECT 1 # a;b\n; SELECT 2", []string{"SELECT 1 # a;b", "SELECT 2"}},
		{
			"block comment",
			"SELECT /* a;b */ 1; SELECT 2",
			[]string{"SELECT /* a;b */ 1", "SELECT 2"},
		},
		{"comment-only piece", "SELECT 1; /* only */ -- this\n# and this\n;", []string{"SELECT 1"}},
		{"leading comment kept", "-- head\nSELECT 1", []string{"-- head\nSELECT 1"}},
		{"executable comment", "/*!40101 SET x = 1 */;", []string{"/*!40101 SET x = 1 */"}},
		{"open string", "SELECT 'a; SELECT 2", []string{"SELECT 'a; SELECT 2"}},
		{"open identifier", "SELECT `a; SELECT 2", []string{"SELECT `a; SELECT 2"}},
		{"open block", "SELECT 1; /* a; b", []string{"SELECT 1"}},
		{"open line", "SELECT 1; # a; b", []string{"SELECT 1"}},
		{"trailing backslash", `SELECT '\`, []string{`SELECT '\`}},
		{"lone slash", "SELECT 4/2; SELECT 2", []string{"SELECT 4/2", "SELECT 2"}},
		{"unicode", "SELECT 'é;'; SELECT '☃'", []string{"SELECT 'é;'", "SELECT '☃'"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, c.want, mysqlsplit.Split(c.in, mysqlsplit.Mode{}))
		})
	}
}

// NO_BACKSLASH_ESCAPES makes a backslash in a string an ordinary
// character, and ANSI_QUOTES makes double quotes quote an identifier, in
// which a backslash is ordinary too.
//
//nolint:funlen // one case per mode and quote.
func TestSplit_Modes(t *testing.T) {
	t.Parallel()

	nbe := mysqlsplit.Mode{NoBackslashEscapes: true}
	ansi := mysqlsplit.Mode{ANSIQuotes: true}

	cases := []struct {
		name string
		in   string
		want []string
		mode mysqlsplit.Mode
	}{
		{
			"no backslash escapes",
			`SELECT '\' # '; DROP TABLE victim; -- '`,
			[]string{`SELECT '\' # '; DROP TABLE victim; -- '`},
			nbe,
		},
		{
			"no backslash escapes, double quotes",
			`SELECT "\"; SELECT 2`,
			[]string{`SELECT "\"`, "SELECT 2"},
			nbe,
		},
		{
			"no backslash escapes, doubled quote",
			`SELECT 'a\''b;'; SELECT 2`,
			[]string{`SELECT 'a\''b;'`, "SELECT 2"},
			nbe,
		},
		{
			"ansi quotes",
			`SELECT 1 AS "\" # "; DROP TABLE victim; -- "`,
			[]string{`SELECT 1 AS "\" # "; DROP TABLE victim; -- "`},
			ansi,
		},
		{
			"default reads the ansi text as a string",
			`SELECT 1 AS "\" # "; DROP TABLE victim; -- "`,
			[]string{`SELECT 1 AS "\" # "`, "DROP TABLE victim"},
			mysqlsplit.Mode{},
		},
		{
			"ansi quotes, single quotes still escape",
			`SELECT '\'; x'; SELECT 2`,
			[]string{`SELECT '\'; x'`, "SELECT 2"},
			ansi,
		},
		{
			"ansi quotes, doubled",
			`SELECT "a"";b"; SELECT 2`,
			[]string{`SELECT "a"";b"`, "SELECT 2"},
			ansi,
		},
		{
			"default reads the escaped quote",
			`SELECT '\' # '; DROP TABLE victim; -- '`,
			[]string{`SELECT '\' # '`, "DROP TABLE victim"},
			mysqlsplit.Mode{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, c.want, mysqlsplit.Split(c.in, c.mode))
		})
	}
}

// ParseMode finds the two modes that matter in a value of @@sql_mode, in
// any case and with spaces, and nothing else.
func TestParseMode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, mysqlsplit.Mode{}, mysqlsplit.ParseMode(""))
	assert.Equal(
		t,
		mysqlsplit.Mode{},
		mysqlsplit.ParseMode("ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION"),
	)
	assert.Equal(
		t,
		mysqlsplit.Mode{NoBackslashEscapes: true, ANSIQuotes: true},
		mysqlsplit.ParseMode("REAL_AS_FLOAT, ansi_quotes,NO_BACKSLASH_ESCAPES"),
	)
	assert.Equal(
		t,
		mysqlsplit.Mode{ANSIQuotes: true},
		mysqlsplit.ParseMode("REAL_AS_FLOAT,PIPES_AS_CONCAT,ANSI_QUOTES,IGNORE_SPACE,ANSI"),
	)
}

// FuzzSplit checks, for any text and mode, that Split does not panic, returns the
// same result twice, returns statements that are non-empty, trimmed of
// white space, substrings of the input in order, and that joining them
// on a separator that cannot fall in a comment and splitting again gives
// them back, unless a statement ends in `--`.
func FuzzSplit(f *testing.F) {
	for _, seed := range []string{
		"",
		"SELECT 1; SELECT 2",
		"SELECT 'a;b'; SELECT `c;d`; SELECT \"e;f\"",
		"SELECT 1 -- x;\n; # y;\n /* z; */ SELECT 2",
		"SELECT 'it''s'; SELECT '\\''",
		"SELECT 1 --;SELECT 2",
		"/*!50000 SET x=1*/; DELIMITER $$",
		"CREATE TABLE t (v VARCHAR(10) DEFAULT 'a;b'); INSERT INTO t VALUES ('hunter2;')",
		"'", "`", "\"", "/*", "--", "#", "\\", "--;",
		"SELECT '\\' # '; DROP TABLE victim; -- '",
		"SELECT 1 AS \"\\\" # \"; DROP TABLE victim; -- \"",
	} {
		for _, mode := range []uint8{0, 1, 2, 3} {
			f.Add(seed, mode)
		}
	}

	f.Fuzz(func(t *testing.T, in string, bits uint8) {
		mode := mysqlsplit.Mode{NoBackslashEscapes: bits&1 != 0, ANSIQuotes: bits&2 != 0}

		got := mysqlsplit.Split(in, mode)
		require.Equal(t, got, mysqlsplit.Split(in, mode))

		rest := in

		for _, stmt := range got {
			require.NotEmpty(t, stmt)
			require.Equal(t, strings.Trim(stmt, " \t\n\r\f\v"), stmt)

			i := strings.Index(rest, stmt)
			require.GreaterOrEqual(t, i, 0, "%q is not in order in %q", stmt, in)

			rest = rest[i+len(stmt):]
		}

		// A `--` that a semicolon followed was code, and turns into a
		// comment once the newline of the separator follows it, as it
		// would for MySQL at the end of the statement sent alone.
		if !slices.ContainsFunc(got, func(s string) bool { return strings.HasSuffix(s, "--") }) {
			require.Equal(t, got, mysqlsplit.Split(strings.Join(got, "\n;\n"), mode))
		}
	})
}
