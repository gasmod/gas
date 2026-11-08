// Package testutil is the auth module's test harness: real database and
// migration services on an in-memory SQLite database or on a PostgreSQL
// or MySQL container, and small fakes for the services the stores need.
//
// # Usage
//
// A store test runs once per backend:
//
//	func TestStore(t *testing.T) {
//		testutil.ForEachBackend(t, func(t *testing.T, b testutil.Backend) {
//			store, err := sessionstore.New()(b.DB, testutil.NewNopLogger(), b.Migrations)
//			// ... Init the store, then b.Migrations.RunPending() ...
//		})
//	}
//
// # Backends
//
// [ForEachBackend] runs its function in one subtest per [Backend], named
// after it: `sqlite`, then `postgres`, then `mysql` when [MySQLEnv] is
// set. [SetupSQLite] opens an in-memory SQLite database with one
// connection, since each connection to `:memory:` has a database of its
// own. [PostgresBackend] starts a `postgres:16-alpine` container for the
// test, and skips under -short.
// Each backend's database and migration services are real, from the
// database and migrate modules, and are closed when the test ends.
// SetupSQLite, PostgresBackend, [SetupPostgres], [MySQLBackend] and
// [SetupMySQL] take a testing.TB, so a fuzz target can call them;
// ForEachBackend needs a *testing.T for its subtests.
//
// # MySQL
//
// The MySQL backend is opt-in: it joins ForEachBackend only when
// `GAS_TEST_MYSQL` ([MySQLEnv]) is set to any non-empty value, so that no
// run starts a MySQL container, or pulls its image, unasked. Without it,
// ForEachBackend has no `mysql` subtest, and MySQLBackend and SetupMySQL
// skip the test. With it, [MySQLBackend] starts a [MySQLImage]
// (`mysql:8.4`) container for the test, and skips under -short. The image
// is pinned, so a machine that has it never pulls another; Docker pulls
// it once on a machine that does not. The server runs with
// `--innodb-use-native-aio=OFF`, since parallel servers with native AIO
// exhaust the host's `fs.aio-max-nr` and fail to start, and with its data
// directory on a tmpfs.
//
// The connection string carries `parseTime=true`, so that the driver
// scans `DATETIME` into time.Time, as the stores need. It leaves
// `multiStatements` off, as gas/database does by default: the migrate
// module sends the statements of a MySQL migration one at a time. The
// driver's `loc` is left at its default, UTC, which is also the
// container's time zone. The database service opens the connection
// string with the dialect `mysql`, which its Driver reports, the name the
// stores pick their dialect by.
//
// # Docker
//
// The PostgreSQL and MySQL backends need Docker. When Docker is not
// available the test is skipped, unless `GAS_TEST_REQUIRE_DOCKER` is set
// to any non-empty value, as CI sets it: then the test fails, so that a
// missing Docker cannot pass as a run of skipped tests.
//
// # Fakes
//
// [FakeService] is a gas.Service with a fixed name, for the stores' Init.
// [FakeDB] is a gas.DatabaseProvider that reports a given driver name and
// *sql.DB, for the stores' dialect and nil-database checks; its other
// methods fail with [ErrFakeDB].
//
// # Logging
//
// [RecordingLogger] is a gas.Logger that keeps every event it sends as an
// [Entry], returned in order by Entries; its zero value is ready. A
// logger from its With, once the context's Logger is called, records into
// the same entries: Entries on either returns the events of both. Each of
// its events starts with the fields given to the context, and with those
// of the logger With was called on before them; a field the event sets
// itself replaces one of the same key.
//
// # Documentation
//
// [DocLines] and [DocBlock] read the code blocks of a Go file's doc
// comments, the comment lines that start with `//` and a tab, so a test
// can check the error messages a package documents against the errors it
// returns. DocLines returns every such line that starts with a given
// prefix, such as a service's name; DocBlock returns the first block
// after a comment line that holds a given marker. Both return the lines
// without the `//` and the tab, and fail the test when the file cannot be
// read; DocBlock fails it too when there is no such block.
package testutil
