// Package migrate applies the database migrations that a Gas app's
// services register, in one global version order, and tracks them in a
// table of the database. It runs on PostgreSQL, MySQL and SQLite through
// a gas.DatabaseProvider, such as gas/database's Service, and holds a
// lock in the database while it changes the schema, so that several
// instances of the app can start at once. [Service] implements
// gas.Service, gas.MigrationManager and gas.ReadyReporter.
//
// # Usage
//
// In an app, the container builds the service from [New], services
// register their migrations in their Init, and the worker calls
// RunPending once every service is initialized. By hand:
//
//	svc, err := migrate.New()(db, logger)
//	if err != nil {
//		return err
//	}
//	if err := svc.Init(ctx); err != nil {
//		return err
//	}
//	defer svc.Close(ctx)
//
//	svc.Register(owner, gas.Migration{
//		Version:     "001",
//		Description: "create notes",
//		Up:          "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)",
//		Down:        "DROP TABLE notes",
//	})
//
//	err = svc.RunPendingContext(ctx)
//
// db is a gas.DatabaseProvider and owner the gas.Service the migration
// belongs to. After RunPendingContext the table `notes` exists, and
// [Service.Applied] returns one row, version `001`, owned by owner.
//
// # Registration
//
// [Service.Register] adds one migration, owned by the given service,
// whose name is recorded with it; the migration's own Service field is
// replaced by that service. [Service.RegisterSlice] registers several.
// Neither can return an error, so a problem with a registration is
// recorded instead, and [Service.RunPending], [Service.Down] and
// [Service.CheckReady] return the first one recorded until the process
// ends; the migration is not registered. The problems are:
//
//   - a nil owner service: [ErrNilService]. Register(nil, m) does not
//     panic.
//   - an empty version: [ErrInvalidVersion].
//   - a version registered before by another service, or by the same
//     service with another description, up SQL or down SQL:
//     [ErrDuplicateVersion]. The first registration stays.
//   - an unknown directive in the up or down SQL: [ErrUnknownDirective].
//
// All-digit versions registered beside other versions are
// [ErrInvalidVersion] too, reported for the first other version in
// version order. That is checked when the migrations are read, by
// RunPending, Down and CheckReady, so registering one more migration can
// make a set mixed. So is a directive the database does not support,
// `-- gas:no-transaction` on SQLite, which is [ErrUnsupportedDirective],
// reported for the first such migration in version order, its up SQL
// before its down SQL, once no other problem is.
//
// Registering a migration again with the same owner, version,
// description and SQL does nothing, so a service that registers in its
// Init can be restarted. Registrations outlive Close: they belong to the
// app, not to one Init.
//
// [Service.RegisterFS] registers the files of an fs.FS, walked from its
// root, subdirectories included. A file whose base name ends in
// `.up.sql` holds a migration's up SQL, and the file of the same base name
// ending in `.down.sql`, in any directory, its down SQL; other files are
// ignored. The rest of the base name is `<version>_<description>`: the
// version is up to the first underscore, or the whole name when there is
// none, and in the description each underscore becomes a space. So
// `20250216001_create_users.up.sql` is version `20250216001`, description
// `create users`. A nil fs.FS is [ErrNilFS], returned and also recorded
// as Register records its problems. RegisterFS returns an error, and
// registers nothing, for a nil service ([ErrNilService]), a file it
// cannot read, and these
// file problems, wrapping [ErrInvalidMigrationFile]: an empty version, two
// up files of one version, two down files of one base name, an up file
// without its down file, and a down file without its up file. An empty
// down file is allowed: it means the migration has no down SQL. The
// migrations it reads are then registered as RegisterSlice registers
// them.
//
// # Versions
//
// A version is any non-empty text. When every registered version is all
// ASCII digits, versions are ordered by their value: by the number of
// digits without leading zeros, then by those digits, so `9` comes
// before `10` and `0010` after `9`; two versions of one value, such as
// `01` and `1`, are ordered by their text. Otherwise every version is
// ordered by its text, byte by byte, and all-digit versions are refused
// beside them, as described under Registration, since neither order
// would suit both. Rows of the tracking table that mix the two kinds,
// from an earlier release, are ordered with all-digit versions first.
//
// A migration that is registered after a higher version was applied is
// still applied, by the next run: what counts is whether its version has
// a row, not where it falls.
//
// # Applying
//
// [Service.RunPending] calls [Service.RunPendingContext] with
// context.Background(). It takes the migration lock, as described under
// Concurrency, reads the tracking table, and applies each registered
// migration whose version has no row, in version order. Rows of versions
// no service registers are left alone. While any row is dirty it applies
// nothing and returns [ErrDirty]. The first migration that fails stops
// the run: those before it stay applied.
//
// How a migration is applied depends on the dialect:
//
//   - PostgreSQL: one transaction runs the up SQL and inserts the row. The
//     up SQL is sent as one query; PostgreSQL runs a query of several
//     statements in it.
//   - SQLite: one transaction, started with BEGIN IMMEDIATE, runs the up
//     SQL and inserts the row, and commits before the next migration
//     starts. Inside it the tracking table is read again, so that a
//     migration another instance applied meanwhile is skipped, as
//     described under Concurrency. The up SQL is sent as one query.
//   - MySQL: the row is first inserted with `dirty` set, on its own; then
//     one transaction runs the up SQL's statements, one at a time, as
//     described under MySQL statements, clears the mark, and commits.
//     MySQL commits the transaction before and after each DDL statement,
//     such as CREATE TABLE, so a migration's DDL is not rolled back when a
//     later statement fails; the mark written first makes sure that a
//     failure, a canceled context or a crash between the two leaves the
//     migration dirty, never applied in part with a clean row or no row.
//
// When a PostgreSQL or SQLite migration fails, its transaction is rolled
// back, the database is as it was before the migration, and the
// migration is marked dirty: a row is inserted with `dirty` set. A
// version that has a row when it is to be marked dirty, which the lock
// should make impossible, keeps its row as it is: a migration another
// runner applied is never marked dirty. When a MySQL migration fails, its
// row stays dirty, and the migration may have left part of its changes
// behind, which must be repaired by hand before ClearDirty, as described
// under Dirty migrations.
//
// The service logs `migration applied` at info level for each migration,
// with `version`, `owner` (the owner service's name) and `duration`;
// `migration marked dirty` at warn level with `version` and `owner`, and
// never the SQL or the driver's error, when a failed migration's row is
// left dirty; and `no pending migrations` at debug level when there is
// nothing to apply. The other logs are `migration reverted` under Down,
// `dirty mark cleared` under Dirty migrations, the two lock logs under
// Concurrency, and `service initialized` and `service closed` under
// Lifecycle.
//
// # Directives
//
// A migration's up or down SQL can open with directives: comment lines of
// the form `-- gas:<name>`, with white space after `--`, among its leading
// lines that are blank or start with `--`. The first other line ends
// them. MySQL reads `--` as a comment only when white space follows it,
// so `--gas:no-split` is no directive, and on MySQL not a comment either.
// A directive applies to the SQL it opens, up or down. An unknown name is
// a registration problem that names the line, not its text. The
// directives are:
//
//   - `-- gas:no-transaction`: on PostgreSQL and MySQL, the SQL runs
//     without a transaction. Its row is written dirty first, on its own,
//     and marked clean, or deleted for a revert, after the SQL succeeds,
//     as on MySQL; a failure, a canceled context or a crash leaves the
//     migration dirty, and what ran before the failure stays. On
//     PostgreSQL this is how CREATE INDEX CONCURRENTLY runs; it must be
//     the only statement of its migration, since PostgreSQL runs a query
//     of several statements as one transaction. SQLite does not support
//     it: a migration that has it is a registration problem there,
//     [ErrUnsupportedDirective], as described under Registration. SQLite
//     holds no lock across a migration, so a row written dirty ahead of
//     one would make another instance that starts meanwhile fail; and the
//     statements SQLite refuses in a transaction, VACUUM and PRAGMA
//     foreign_keys, would still not run in a migration.
//   - `-- gas:no-split`: on MySQL, the SQL is sent as one statement
//     instead of being split, for a stored routine or a trigger whose body
//     holds semicolons. It has no effect on the other dialects.
//
// # MySQL statements
//
// gas/database leaves the MySQL driver's multiStatements off unless the
// DSN turns it on, so a query may hold one statement. On MySQL the
// service therefore splits a migration's SQL into statements at each
// semicolon outside quotes, backticks and comments (`--` followed by
// white space, `#`, and `/* */`), and sends them one at a time. It reads
// them as MySQL does in the session's SQL mode, which it reads from
// @@sql_mode on its connection before each migration: with
// NO_BACKSLASH_ESCAPES a backslash in a string is an ordinary character,
// and with ANSI_QUOTES, which the mode ANSI includes, `"` quotes an
// identifier. A migration that changes sql_mode changes how the
// migrations after it are split, not its own statements. A piece that holds only white space
// and comments is not sent. The splitter knows nothing of the client's
// DELIMITER command: SQL that needs it takes `-- gas:no-split`, and must
// then be one statement, or the DSN must allow several. PostgreSQL and
// SQLite are never split.
//
// # Concurrency
//
// [Service.Init], [Service.RunPendingContext], [Service.DownContext] and
// [Service.ClearDirty] each take the migration lock in the database, on
// one connection they hold for the whole call and run all their work on,
// and read the tracking table only once they hold it. So of several
// instances of the app starting at once against one database, one applies
// the pending migrations while the others wait, and then find them
// applied. The lock is:
//
//   - PostgreSQL: the session-level advisory lock on [PostgresLockKey],
//     taken with pg_try_advisory_lock and released with
//     pg_advisory_unlock. Advisory locks belong to one database, so apps
//     on other databases of the server do not wait for each other.
//   - MySQL: the user-level lock named [MySQLLockPrefix] followed by the
//     MD5 of the database name in hex, taken with GET_LOCK and released
//     with RELEASE_LOCK. User-level locks belong to the whole server; the
//     name makes the lock one per database. GET_LOCK's 1 is the lock
//     taken, 0 a wait that ended, and NULL an error.
//   - SQLite: there is no lock held for the whole call. Each write takes
//     the database file's write lock itself, in a transaction started with
//     BEGIN IMMEDIATE: each migration, each dirty mark, the tracking
//     table's creation and ClearDirty's delete. Inside it the tracking
//     table is read again: a migration whose row another instance wrote
//     meanwhile is skipped, a revert whose row is gone too, and a dirty
//     row is [ErrDirty]. So several instances may share the pending
//     migrations between them, each applying one at a time, and every one
//     is applied once. A connection that meets another's write lock gets
//     SQLITE_BUSY, which the service tells by the driver error's Code
//     method, as modernc.org/sqlite has, and tries again; COMMIT, which
//     waits for readers to finish, is tried again the same way, keeping
//     the transaction open. With a driver whose errors have no Code
//     method, SQLITE_BUSY is returned as an error. gas/database adds a
//     busy timeout of 5 seconds to a SQLite DSN that sets none; with one,
//     each attempt waits that long in SQLite before it fails, which can
//     stretch the lock timeout.
//
// When the lock is held elsewhere, the service logs `waiting for the
// migration lock` at info level, with `timeout`, once per wait, and tries
// again: MySQL waits in GET_LOCK, a second at a time; PostgreSQL and
// SQLite are tried every 100ms. The wait, getting the connection
// included, ends when the call's context ends, with an error wrapping the
// context's error, or when the lock timeout passes, with an error
// wrapping [ErrLockTimeout]; nothing is marked dirty. On SQLite each
// write waits up to the lock timeout, and a failed wait for a migration
// names it, as `apply 001 (owner app): acquire lock: lock timed out`; a
// COMMIT that waits for readers longer fails the migration, as `commit: `
// and the same causes. The lock timeout is [DefaultLockTimeout] unless
// [WithLockTimeout] sets it.
//
// The lock is released when the call ends, however it ends, a panic
// included, with a context that keeps the call's values but not its
// cancellation, bounded by 30 seconds, which also bounds marking a
// migration dirty and rolling back after the call's context ended. When
// releasing fails, this is logged at warn level as `migration lock
// release failed, closing its connection`, and the call's result stands.
// On PostgreSQL and MySQL the call's connection is then closed, never
// returned to the pool, so that no lock, open transaction or session
// setting a migration made, such as `SET FOREIGN_KEY_CHECKS = 0` on MySQL
// or `SET search_path` on PostgreSQL, reaches the app's later queries.
// Closing it also releases a lock that releasing could not. On SQLite the
// connection goes back to the pool, since closing a connection to an
// in-memory database, such as `:memory:` with one connection, loses the
// database; a PRAGMA or temporary table a migration made stays on it, so
// a migration must not change a session PRAGMA, such as foreign_keys or
// busy_timeout, that the app's later queries expect as it was. A
// transaction a panic leaves open is rolled back first, so a connection
// never goes back to the pool inside one. A SQLite connection whose
// state is unknown after a failed ROLLBACK is closed all the same.
//
// The lock belongs to a database session, so the connection must reach
// one server session for the whole call. A connection pooler in session
// mode does that. PgBouncer in transaction or statement mode does not: it
// may run the lock, the migrations and the unlock on different server
// sessions, so the advisory lock protects nothing and its unlock fails;
// point migrate at the server, or at a session-mode pool, instead. A
// migration's session settings, such as USE on MySQL or SET search_path
// on PostgreSQL, apply to the tracking table's statements that follow it
// in the call, so a migration must not move them to another database or
// schema.
//
// A Service is safe for concurrent use. Init and Close run one at a time.
// Calls on one Service that take the lock take a connection each, so they
// wait for each other like separate instances do. CheckReady and Applied
// take no lock. A run goes on with the state it started with when Close
// runs beside it.
//
// # Dirty migrations
//
// A dirty row blocks RunPending and Down, which return [ErrDirty] naming
// the dirty versions, and makes CheckReady fail the same way, until
// [Service.ClearDirty] removes it. ClearDirty takes the lock and deletes
// the version's row when it is dirty, so that the next RunPending applies
// the migration again; a version that is not dirty, or has no row, is
// [ErrNotDirty], and nothing changes. ClearDirty is not part of
// gas.MigrationManager: a maintenance command builds the service to call
// it.
//
// On PostgreSQL and SQLite a migration that failed in its transaction was
// rolled back, so fixing its SQL and clearing the mark is enough. On
// MySQL, and for a `-- gas:no-transaction` migration on PostgreSQL, a
// dirty migration may have left part of its changes behind, such as a
// table its first statement created; the row is dirty from before the
// migration ran, so a canceled context or a crash leaves it dirty too,
// even one that comes before any of the migration's SQL ran, and a dirty
// revert may have left part of its down SQL applied: repair the schema
// by hand, so that it is as it was before the migration (or, for a
// revert, as it was after it), and then clear the mark, or the next run
// fails on what is left. When nothing of the migration ran, clearing the
// mark is all there is to do. The service logs `dirty mark cleared` at
// info level, with `version`, when ClearDirty removes a mark.
//
// Every write of a tracking row that must change exactly one row, the
// row written ahead of a migration, the mark cleared after it, and the
// row inserted or deleted with a migration in its transaction, checks
// that it did. Any other count fails the run with [ErrTrackingRowLost],
// wrapping [ErrMigrationFailed] once the migration ran: the row was
// changed from outside the service, such as deleted by hand, while the
// migration ran. A migration whose clean mark finds its row gone may be
// applied with no row, and would be applied again by the next run; no
// `migration marked dirty` is logged for it, since there is no mark. Check
// the schema by hand and write the row, or undo the migration, before the
// next run.
//
// # Down
//
// [Service.Down] calls [Service.DownContext] with context.Background().
// DownContext reverts the n highest versions of the tracking table,
// highest first in version order, which need not be the order they were
// applied in. An n of zero or less does nothing and returns nil, once the
// service is initialized; an n larger than the number of rows reverts
// them all. It takes the lock and reads the table as RunPending does,
// returns [ErrDirty] while a row is dirty, and then, before it reverts
// anything, checks every version it is to revert: one no service
// registers is [ErrUnregisteredVersion], and one whose down SQL is empty
// or white space is [ErrNoDownSQL]. Each migration is then reverted with
// the delete of its row, as a migration is applied, with the down SQL's
// directives: on PostgreSQL and SQLite in one transaction, which a
// failure rolls back, marking nothing dirty; on MySQL, and with
// `-- gas:no-transaction` on PostgreSQL, the row is marked dirty first and
// deleted after the down SQL succeeds, so a failed revert leaves it dirty
// and logs `migration marked dirty`, since part of the down SQL may have
// run. A failed revert stops the call; those before it stay reverted. On
// SQLite a revert whose row another instance deleted meanwhile is
// skipped, so two instances that revert at once revert the versions they
// read once. The service logs `migration reverted` at info level for
// each, with `version`, `owner` and `duration`.
//
// # Readiness
//
// [Service.CheckReady] returns nil when every registered migration has a
// row and none is dirty. Otherwise it returns, in this order of checks,
// [ErrNotInitialized], the first registration problem, a failure to read
// the tracking table, [ErrDirty], or [ErrPending] with the number of
// pending migrations. It takes no lock, and a migration registered after
// RunPending makes the service unready until the next start applies it.
//
// On PostgreSQL and MySQL a dirty row may be one a run wrote ahead of a
// migration it is running, as described under Applying. So while another
// session holds the migration lock, CheckReady takes the dirty rows as in
// flight, not failed: it logs `dirty migrations in flight` at debug level,
// with `versions`, and goes on to the pending check, so that in a rolling
// deploy the other replicas stay ready while one migrates. That holds for
// a row left dirty by an earlier failure too, for as long as the lock is
// held. Whether the lock is held is read with IS_USED_LOCK on MySQL and
// from pg_locks on PostgreSQL; a failure to read it is returned, after
// `read migration lock: `. Once the lock is free, dirty rows are
// [ErrDirty] again. On SQLite a dirty row is always [ErrDirty]: SQLite
// never writes a row ahead of a migration.
//
// [Service.Applied] returns the rows of the tracking table as
// [AppliedMigration] values, in version order: version, owner service,
// description and dirty mark.
//
// # Lifecycle
//
// [Service.Init] checks the lock timeout ([ErrInvalidLockTimeout] unless
// positive), picks the dialect from the provider's Driver, takes the
// provider's DB, and creates the tracking table under the lock when it
// does not exist. Driver `postgres` and `pgx` are PostgreSQL, `mysql` is
// MySQL, and `sqlite` and `sqlite3` are SQLite; gas/database reports
// `postgres`, `mysql` and `sqlite`. Any other is [ErrUnsupportedDriver],
// which lists the supported names and not the one given. A provider whose
// DB returns nil is [ErrNilDatabase]. Init then logs `service
// initialized` at info level with `dialect`. Init on an initialized
// service returns [ErrAlreadyInitialized] and changes nothing; a failed
// Init leaves the service uninitialized.
//
// [Service.Close] makes the service uninitialized and logs `service
// closed` at info level; it holds no connection to close. Init may run
// again after it, and takes the provider's DB afresh, so restarting the
// database means restarting migrate too. The registrations stay. Until
// Init succeeds, after a failed Init and after Close, RunPending, Down,
// ClearDirty, CheckReady and Applied return [ErrNotInitialized]. [New]'s
// constructor fails with [ErrNilDatabase] for a nil provider and
// [ErrNilLogger] for a nil logger.
//
// # Tracking table
//
// The table [TableName], `__gas_migrations`, holds one row per applied or
// dirty version:
//
//   - `version`: the primary key. On MySQL a VARCHAR(255) with the
//     database's collation, which by default ignores case, so `v1` and
//     `V1` are one version there.
//   - `service`: the owner service's name.
//   - `description`: the migration's description.
//   - `migrate_version` and `module_version`: unused, written empty. They
//     stay because tables of earlier releases have them.
//   - `applied_at`: when the row was written, set by the database. The
//     service never reads it, so the MySQL driver's parseTime does not
//     matter.
//   - `dirty`: whether the migration is marked dirty.
//
// Init creates the table with CREATE TABLE IF NOT EXISTS and never
// changes one that exists.
//
// # Upgrading from v0.5
//
// The tracking table of earlier releases works as it is: Init leaves an
// existing table unchanged, and its rows, `applied_at` included, are read
// as they were written. What changed is the order of versions:
//
//   - All-digit versions are ordered by their value, where v0.5 ordered
//     every version by its text. A set whose all-digit versions all have
//     the same number of digits, such as timestamps, keeps its order.
//   - Down(n) reverts the n highest versions in the new order, so with
//     all-digit versions of different lengths, such as `9` and `10`, it
//     may revert a different version than v0.5 would.
//   - On a fresh database, such versions are applied in the new order: `9`
//     before `10`, where v0.5 applied `10` first.
//   - Registering all-digit versions beside other versions, which v0.5
//     ordered by text, is now [ErrInvalidVersion], reported by RunPending,
//     Down and CheckReady, so such an app fails to start until its
//     versions are renamed. Tracking tables that already mix them are
//     read, with the all-digit versions first, as under Versions.
//
// # Errors
//
// The sentinel errors, to be tested with [errors.Is]:
//
//   - [ErrNilDatabase]: New's constructor got a nil provider, or Init got
//     a nil DB from it.
//   - [ErrNilLogger]: New's constructor got a nil logger.
//   - [ErrNotInitialized], [ErrAlreadyInitialized]: as under Lifecycle.
//   - [ErrUnsupportedDriver], [ErrInvalidLockTimeout]: Init could not
//     start.
//   - [ErrNilService], [ErrNilFS], [ErrInvalidVersion],
//     [ErrDuplicateVersion], [ErrUnknownDirective],
//     [ErrUnsupportedDirective], [ErrInvalidMigrationFile]: a
//     registration failed, as under Registration.
//   - [ErrDirty]: a migration is dirty; on MySQL, or with
//     `-- gas:no-transaction`, its changes may be partly applied and need
//     repair by hand before ClearDirty.
//   - [ErrTrackingRowLost]: a tracking row changed from outside the
//     service while a migration ran, as under Dirty migrations.
//   - [ErrNotDirty]: ClearDirty got a version that is not dirty.
//   - [ErrPending]: CheckReady found migrations to apply.
//   - [ErrUnregisteredVersion], [ErrNoDownSQL]: Down cannot revert a
//     version.
//   - [ErrMigrationFailed]: a migration's SQL, or writing its row, failed
//     once it had started. It wraps the driver's error. A failure before
//     anything ran, such as taking SQLite's write lock or writing the
//     dirty mark that comes first on MySQL, does not wrap it.
//   - [ErrLockTimeout]: the lock was not taken within the lock timeout.
//
// Every error the package returns starts with [ServiceName] once, names
// what failed, a version with its owner when there is one, and wraps the
// sentinel and then the cause:
//
//	apply 002 (owner gas/auth/session): migration failed: <driver error>
//
// The errors never hold a migration's SQL, apart from what the driver's
// own text quotes of it, such as the token a syntax error is near. The
// driver's errors are wrapped so that an operator sees why a migration
// failed. When the provider has a method `SafeError(error) error`, as
// gas/database's Service has, every driver error passes through it first,
// so that a connection-level failure is replaced by one that holds no
// user, host or database name. With another provider, driver errors are
// wrapped as the driver returned them.
//
// The errors, as returned, with SQLite's text for the driver errors:
//
//	gas/migrate: database is nil
//	gas/migrate: logger is nil
//	gas/migrate: service not initialized
//	gas/migrate: service already initialized
//	gas/migrate: unsupported driver: supported are postgres, pgx, mysql, sqlite and sqlite3
//	gas/migrate: invalid lock timeout: must be positive
//	gas/migrate: register 001: service is nil
//	gas/migrate: register: service is nil
//	gas/migrate: register (owner app): invalid version: empty
//	gas/migrate: register v1 (owner app): invalid version: all-digit and other versions are mixed
//	gas/migrate: register 001 (owner billing): duplicate version: also registered by app
//	gas/migrate: register 001 (owner app): duplicate version: registered again with different content
//	gas/migrate: register 001 (owner app): up SQL: unknown directive on line 1
//	gas/migrate: register 001 (owner app): down SQL: unknown directive on line 2
//	gas/migrate: register 001 (owner app): up SQL: unsupported directive: no-transaction on sqlite
//	gas/migrate: register files: service is nil
//	gas/migrate: register files (owner app): file system is nil
//	gas/migrate: register files (owner app): invalid migration file: _x.up.sql has an empty version
//	gas/migrate: register files (owner app): invalid migration file: b/001_y.up.sql repeats the version of a/001_x.up.sql
//	gas/migrate: register files (owner app): invalid migration file: b/001_x.down.sql repeats a/001_x.down.sql
//	gas/migrate: register files (owner app): invalid migration file: 001_x.up.sql has no down file
//	gas/migrate: register files (owner app): invalid migration file: 001_x.down.sql has no up file
//	gas/migrate: register files (owner app): read files: open .: file does not exist
//	gas/migrate: acquire lock: lock timed out
//	gas/migrate: apply 001 (owner app): acquire lock: lock timed out
//	gas/migrate: acquire lock: context canceled
//	gas/migrate: dirty migrations: 001, 002
//	gas/migrate: pending migrations: 2
//	gas/migrate: apply 001 (owner app): migration failed: SQL logic error: no such table: missing (1)
//	gas/migrate: revert 001 (owner app): migration failed: SQL logic error: no such table: missing (1)
//	gas/migrate: revert 001 (owner gone): version not registered
//	gas/migrate: revert 001 (owner app): no down SQL
//	gas/migrate: clear 001: migration not dirty
package migrate
