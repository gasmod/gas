# github.com/gasmod/gas/migrate

```go
import "github.com/gasmod/gas/migrate"
```

Package migrate applies the database migrations that a Gas app's
services register, in one global version order, and tracks them in a
table of the database.

It runs on PostgreSQL, MySQL and SQLite through a gas.DatabaseProvider,
such as gas/database's Service. It holds a lock in the database while it
changes the schema, so that several instances of the app can start at
once. `Service` implements gas.Service, gas.MigrationManager and
gas.ReadyReporter.

## Usage

In an app, the container builds the service from `New`, services
register their migrations in their Init, and the worker calls
RunPending once every service is initialized. By hand:

```go
svc, err := migrate.New()(db, logger)
if err != nil {
	return err
}
if err := svc.Init(ctx); err != nil {
	return err
}
defer svc.Close(ctx)

svc.Register(owner, gas.Migration{
	Version:     "001",
	Description: "create notes",
	Up:          "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)",
	Down:        "DROP TABLE notes",
})

err = svc.RunPendingContext(ctx)
```

db is a gas.DatabaseProvider and owner the gas.Service the migration
belongs to. After RunPendingContext the table `notes` exists, and
`Service.Applied` returns one row, version `001`, owned by owner.

## Registration

`Service.Register` adds one migration, owned by the given service. The
service's name is recorded with the migration, and the migration's own
Service field is replaced by that service. `Service.RegisterSlice`
registers several.

Neither can return an error, so a problem with a registration is
recorded instead, and the migration is not registered.
`Service.RunPending`, `Service.Down` and `Service.CheckReady` return the
first problem recorded, until the process ends. The problems are:

- A nil owner service: `ErrNilService`. Register(nil, m) does not
panic.
- An empty version: `ErrInvalidVersion`.
- A version registered before by another service, or by the same
service with another description, up SQL or down SQL:
`ErrDuplicateVersion`. The first registration stays.
- An unknown directive in the up or down SQL: `ErrUnknownDirective`.

Two more problems are checked only when the migrations are read, by
RunPending, Down and CheckReady:

- All-digit versions registered beside other versions:
`ErrInvalidVersion`, reported for the first other version in version
order. Since it is checked on read, registering one more migration
can make a set mixed.
- A directive the database does not support, `-- gas:no-transaction`
on SQLite: `ErrUnsupportedDirective`, reported for the first such
migration in version order, its up SQL before its down SQL, once no
other problem is.

Registering a migration again with the same owner, version, description
and SQL does nothing, so a service that registers in its Init can be
restarted. Registrations outlive Close: they belong to the app, not to
one Init.

## Migration files

`Service.RegisterFS` registers the files of an fs.FS, walked from its
root, subdirectories included:

- A file whose base name ends in `.up.sql` holds a migration's up SQL.
- The file of the same base name ending in `.down.sql`, in any
directory, holds its down SQL. An empty down file is allowed: it
means the migration has no down SQL.
- Other files are ignored.

The rest of the base name is `<version>_<description>`. The version is
up to the first underscore, or the whole name when there is none. In
the description each underscore becomes a space. So
`20250216001_create_users.up.sql` is version `20250216001`, description
`create users`.

RegisterFS returns an error, and registers nothing, for:

- A nil service: `ErrNilService`.
- A nil fs.FS: `ErrNilFS`. It is returned, and also recorded as
Register records its problems.
- A file it cannot read.
- These file problems, wrapping `ErrInvalidMigrationFile`: an empty
version, two up files of one version, two down files of one base
name, an up file without its down file, and a down file without its
up file.

Otherwise the migrations it reads are registered as RegisterSlice
registers them.

## Versions

A version is any non-empty text. Versions are ordered in one of two
ways:

- When every registered version is all ASCII digits, versions are
ordered by their value: by the number of digits without leading
zeros, then by those digits. So `9` comes before `10`, and `0010`
after `9`. Two versions of one value, such as `01` and `1`, are
ordered by their text.
- Otherwise every version is ordered by its text, byte by byte.

All-digit versions are refused beside other versions, as described
under Registration, since neither order would suit both. Rows of the
tracking table that mix the two kinds, from an earlier release, are
ordered with all-digit versions first.

A migration that is registered after a higher version was applied is
still applied, by the next run: what counts is whether its version has
a row, not where it falls.

## Applying

`Service.RunPending` calls `Service.RunPendingContext` with
context.Background(). It takes the migration lock, as described under
Migration lock, and then runs these steps, in order:

1. It reads the tracking table. While any row is dirty, it applies
nothing and returns `ErrDirty`.
2. It records the checksum, as described under Checksums, of each row
that has none and whose version is registered. It does so in version
order, each with an UPDATE of its own that changes the row only
while its checksum is still empty. When it issued any, it reads the
table again, and a row that is dirty now is `ErrDirty`.
3. It compares the checksum of each row that has one and whose version
is registered with the registered up SQL's. While any differs, it
applies nothing and returns `ErrChecksumMismatch`, as described
under Edited migrations.
4. It applies each registered migration whose version has no row, in
version order.

Steps 1 to 3 run on every call, also when nothing is pending. Rows of
versions no service registers are left alone: RunPending never records,
checks or reverts them. The first migration that fails stops the run;
those before it stay applied.

## Transactions

How a migration is applied depends on the dialect:

- PostgreSQL: one transaction runs the up SQL and inserts the row. The
up SQL is sent as one query; PostgreSQL runs a query of several
statements in it.
- SQLite: one transaction, started with BEGIN IMMEDIATE, runs the up
SQL and inserts the row, and commits before the next migration
starts. Inside it the tracking table is read again, so that a
migration another instance applied meanwhile is skipped, as
described under SQLite locking. The up SQL is sent as one query.
- MySQL: the row is first inserted with `dirty` set, on its own. Then
one transaction runs the up SQL's statements, one at a time, as
described under MySQL statements, clears the mark, and commits.

MySQL commits the transaction before and after each DDL statement, such
as CREATE TABLE, so a migration's DDL is not rolled back when a later
statement fails. The mark written first makes sure that a failure, a
canceled context or a crash between the two steps leaves the migration
dirty, never applied in part with a clean row or no row.

When a PostgreSQL or SQLite migration fails, its transaction is rolled
back, and the database is as it was before the migration. The migration
is also marked dirty: a row is inserted with `dirty` set. A version
that has a row when it is to be marked dirty, which the lock should make
impossible, keeps its row as it is: a migration another runner applied
is never marked dirty.

When a MySQL migration fails, its row stays dirty, and the migration may
have left part of its changes behind. They must be repaired by hand
before ClearDirty, as described under Dirty migrations.

## Logging

While it applies migrations, the service logs:

- `migration applied` at info level for each migration, with
`version`, `owner` (the owner service's name) and `duration`.
- `migration marked dirty` at warn level, with `version` and `owner`,
when a failed migration's row is left dirty. It never logs the SQL
or the driver's error.
- `no pending migrations` at debug level when there is nothing to
apply.
- `checksums recorded` at info level, with `count`, the number of rows
step 2 of Applying changed, when that is more than none.
- `checksum mismatch` at warn level for each version step 3 of
Applying finds, with `version`, `owner`, `stored` and `registered`,
the two full checksums. It never logs the SQL.

The other logs are described where they happen:

- `migration reverted` under Down.
- `dirty mark cleared` under Dirty migrations.
- `waiting for the migration lock` under Waiting for the lock, and
`migration lock release failed, closing its connection` under
Releasing the lock.
- `dirty migrations in flight` under Readiness.
- `checksum column added` under Tracking table.
- `service initialized` and `service closed` under Lifecycle.

## Directives

A migration's up or down SQL can open with directives. A directive is a
comment line of the form `-- gas:<name>`, with white space after `--`,
among the SQL's leading lines that are blank or start with `--`. The
first other line ends them. A directive applies to the SQL it opens, up
or down.

MySQL reads `--` as a comment only when white space follows it, so
`--gas:no-split` is no directive, and on MySQL not a comment either. The
name is all that follows `gas:` on the line, so every such leading
comment is a directive: `-- gas: no-split`, with white space after the
colon, and `-- gas: create users`, a plain comment, are unknown names.
An unknown name is a registration problem that names the line, not its
text.

The directives are:

- `-- gas:no-transaction`: on PostgreSQL and MySQL, the SQL runs
without a transaction. SQLite does not support it.
- `-- gas:no-split`: on MySQL, the SQL is sent as one statement
instead of being split, for a stored routine or a trigger whose body
holds semicolons. It has no effect on the other dialects.

With `-- gas:no-transaction`, the row is written dirty first, on its
own, and marked clean, or deleted for a revert, after the SQL succeeds,
as on MySQL. A failure, a canceled context or a crash leaves the
migration dirty, and what ran before the failure stays.

On PostgreSQL, `-- gas:no-transaction` is how CREATE INDEX CONCURRENTLY
runs. It must be the only statement of its migration, since PostgreSQL
runs a query of several statements as one transaction.

On SQLite, a migration that has `-- gas:no-transaction` is a
registration problem, `ErrUnsupportedDirective`, as described under
Registration. SQLite holds no lock across a migration, so a row written
dirty ahead of one would make another instance that starts meanwhile
fail. And the statements SQLite refuses in a transaction, VACUUM and
PRAGMA foreign_keys, would still not run in a migration.

## MySQL statements

gas/database leaves the MySQL driver's multiStatements off unless the
DSN turns it on, so a query may hold one statement. On MySQL the
service therefore splits a migration's SQL into statements, and sends
them one at a time:

- It splits at each semicolon outside quotes, backticks and comments
(`--` followed by white space, `#`, and `/* */`).
- A piece that holds only white space and comments is not sent.
- PostgreSQL and SQLite are never split.

The service reads the statements as MySQL does in the session's SQL
mode, which it reads from @@sql_mode on its connection before each
migration:

- With NO_BACKSLASH_ESCAPES, a backslash in a string is an ordinary
character.
- With ANSI_QUOTES, which the mode ANSI includes, `"` quotes an
identifier.

A migration that changes sql_mode changes how the migrations after it
are split, not its own statements.

The splitter knows nothing of the client's DELIMITER command. SQL that
needs it takes `-- gas:no-split`, and must then be one statement, or the
DSN must allow several.

## Migration lock

`Service.Init`, `Service.RunPendingContext`, `Service.DownContext` and
`Service.ClearDirty` each take the migration lock in the database. Each
takes it on one connection, which it holds for the whole call and runs
all its work on, and reads the tracking table only once it holds the
lock. So of several instances of the app starting at once against one
database, one applies the pending migrations while the others wait, and
then find them applied.

The lock is:

- PostgreSQL: the session-level advisory lock on `PostgresLockKey`,
taken with pg_try_advisory_lock and released with
pg_advisory_unlock. Advisory locks belong to one database, so apps
on other databases of the server do not wait for each other.
- MySQL: the user-level lock named `MySQLLockPrefix` followed by the
MD5 of the database name in hex, taken with GET_LOCK and released
with RELEASE_LOCK. User-level locks belong to the whole server; the
name makes the lock one per database. GET_LOCK's 1 is the lock
taken, 0 a wait that ended, and NULL an error.
- SQLite: there is no lock held for the whole call, as described under
SQLite locking.

## SQLite locking

On SQLite each write takes the database file's write lock itself, in a
transaction started with BEGIN IMMEDIATE. These are the writes:

- Each migration.
- Each dirty mark.
- The tracking table's creation.
- The check for its checksum column, and adding it.
- Each checksum RunPending records.
- ClearDirty's delete.

Inside that transaction the tracking table is read again:

- A migration whose row another instance wrote meanwhile is skipped.
- A revert whose row is gone is skipped too.
- A dirty row is `ErrDirty`.

So several instances may share the pending migrations between them,
each applying one at a time, and every one is applied once.

A connection that meets another's write lock gets SQLITE_BUSY, and
tries again. The service tells SQLITE_BUSY by the driver error's Code
method, as modernc.org/sqlite has. COMMIT, which waits for readers to
finish, is tried again the same way, keeping the transaction open. With
a driver whose errors have no Code method, SQLITE_BUSY is returned as
an error.

gas/database adds a busy timeout of 5 seconds to a SQLite DSN that sets
none. With one, each attempt waits that long in SQLite before it fails,
which can stretch the lock timeout.

## Waiting for the lock

When the lock is held elsewhere, the service logs `waiting for the
migration lock` at info level, with `timeout`, once per wait, and tries
again. MySQL waits in GET_LOCK, a second at a time; PostgreSQL and
SQLite are tried every 100ms. The lock timeout is `DefaultLockTimeout`
unless `WithLockTimeout` sets it.

The wait, getting the connection included, ends in one of two ways, and
either way nothing is marked dirty:

- When the call's context ends, with an error wrapping the context's
error.
- When the lock timeout passes, with an error wrapping
`ErrLockTimeout`.

On SQLite each write waits up to the lock timeout. A failed wait for a
migration names it, as `apply 001 (owner app): acquire lock: lock timed
out`. A COMMIT that waits for readers longer fails the migration, as
`commit: ` and the same causes.

## Releasing the lock

The lock is released when the call ends, however it ends, a panic
included. The release uses a context that keeps the call's values but
not its cancellation, bounded by 30 seconds. That bound also bounds
marking a migration dirty and rolling back after the call's context
ended.

When releasing fails, the service logs `migration lock release failed,
closing its connection` at warn level, and the call's result stands.

On PostgreSQL and MySQL the call's connection is closed when the call
ends, never returned to the pool, the pgxpool of gas/database's ModePgx
included. So no lock, open transaction or
session setting a migration made, such as `SET FOREIGN_KEY_CHECKS = 0`
on MySQL or `SET search_path` on PostgreSQL, reaches the app's later
queries. Closing it also releases a lock that releasing could not.

On SQLite the connection goes back to the pool, since closing a
connection to an in-memory database, such as `:memory:` with one
connection, loses the database. A PRAGMA or temporary table a migration
made stays on it. So a migration must not change a session PRAGMA, such
as foreign_keys or busy_timeout, that the app's later queries expect as
it was.

A transaction a panic leaves open is rolled back first, so a connection
never goes back to the pool inside one. A SQLite connection whose state
is unknown after a failed ROLLBACK is closed all the same.

## Database sessions

The lock belongs to a database session, so the connection must reach
one server session for the whole call. A connection pooler in session
mode does that.

PgBouncer in transaction or statement mode does not. It may run the
lock, the migrations and the unlock on different server sessions, so
the advisory lock protects nothing and its unlock fails. Point migrate
at the server, or at a session-mode pool, instead.

A migration's session settings, such as USE on MySQL or SET search_path
on PostgreSQL, apply to the tracking table's statements that follow it
in the call. So a migration must not move them to another database or
schema.

## Concurrency

A Service is safe for concurrent use. Init and Close run one at a time.
Calls on one Service that take the lock take a connection each, so they
wait for each other like separate instances do. CheckReady and Applied
take no lock. A run goes on with the state it started with when Close
runs beside it.

## Dirty migrations

A dirty row blocks the service until `Service.ClearDirty` removes it:

- RunPending and Down return `ErrDirty`, naming the dirty versions.
RunPending does so before it checks any checksum.
- CheckReady fails the same way.

ClearDirty takes the lock and deletes the version's row when it is
dirty, so that the next RunPending applies the migration again. A
version that is not dirty, or has no row, is `ErrNotDirty`, and nothing
changes. The service logs `dirty mark cleared` at info level, with
`version`, when ClearDirty removes a mark.

ClearDirty is not part of gas.MigrationManager: a maintenance command
builds the service to call it.

What to repair before clearing the mark depends on how the migration
ran:

- On PostgreSQL and SQLite, a migration that failed in its transaction
was rolled back, so fixing its SQL and clearing the mark is enough.
- On MySQL, and for a `-- gas:no-transaction` migration on PostgreSQL,
a dirty migration may have left part of its changes behind, such as
a table its first statement created. A dirty revert may have left
part of its down SQL applied.

In the second case, repair the schema by hand, so that it is as it was
before the migration (or, for a revert, as it was after it), and then
clear the mark, or the next run fails on what is left. The row is dirty
from before the migration ran, so a canceled context or a crash leaves
it dirty too, even one that comes before any of the migration's SQL
ran. When nothing of the migration ran, clearing the mark is all there
is to do.

## Lost tracking rows

These writes of a tracking row must change exactly one row, and check
that they did:

- The row written ahead of a migration.
- The mark cleared after it.
- The row inserted or deleted with a migration in its transaction.

Any other count fails the run with `ErrTrackingRowLost`, wrapping
`ErrMigrationFailed` once the migration ran: the row was changed from
outside the service, such as deleted by hand, while the migration ran.
The UPDATE that records a checksum is no such write, and changes no row
when another instance recorded it first.

A migration whose clean mark finds its row gone may be applied with no
row, and would be applied again by the next run. No `migration marked
dirty` is logged for it, since there is no mark. Check the schema by
hand and write the row, or undo the migration, before the next run.

## Down

`Service.Down` calls `Service.DownContext` with context.Background().
DownContext reverts the n highest versions of the tracking table,
highest first in version order, which need not be the order they were
applied in. An n of zero or less does nothing and returns nil, once the
service is initialized. An n larger than the number of rows reverts
them all.

DownContext runs these steps, in order:

1. It takes the lock and reads the table as RunPending does, and
returns `ErrDirty` while a row is dirty.
2. Before it reverts anything, it checks every version it is to
revert. One no service registers is `ErrUnregisteredVersion`, and
one whose down SQL is empty or white space is `ErrNoDownSQL`.
3. It reverts each migration with the delete of its row, as a
migration is applied, with the down SQL's directives.

How a revert runs depends on the dialect:

- On PostgreSQL and SQLite, in one transaction, which a failure rolls
back, marking nothing dirty.
- On MySQL, and with `-- gas:no-transaction` on PostgreSQL, the row is
marked dirty first and deleted after the down SQL succeeds. So a
failed revert leaves it dirty and logs `migration marked dirty`,
since part of the down SQL may have run.

A failed revert stops the call; those before it stay reverted. On
SQLite a revert whose row another instance deleted meanwhile is
skipped, so two instances that revert at once revert the versions they
read once. The service logs `migration reverted` at info level for
each, with `version`, `owner` and `duration`.

Down neither checks nor records checksums: the down SQL is not part of
one, and an edited migration must stay revertable, as described under
Edited migrations.

## Readiness

`Service.CheckReady` returns nil when every registered migration has a
row and none is dirty. Otherwise it returns the first failure of these
checks, in this order:

1. `ErrNotInitialized`.
2. The first registration problem.
3. A failure to read the tracking table.
4. `ErrDirty`.
5. `ErrPending`, with the number of pending migrations.

It takes no lock, and a migration registered after RunPending makes the
service unready until the next start applies it. It neither checks nor
records checksums: RunPending, at start, is where an edited migration
is caught.

On PostgreSQL and MySQL a dirty row may be one a run wrote ahead of a
migration it is running, as described under Transactions and
Directives. So while another session holds the migration lock,
CheckReady takes the dirty rows as in flight, not failed. It logs
`dirty migrations in flight` at debug level, with `versions`, and goes
on to the pending check, so that in a rolling deploy the other replicas
stay ready while one migrates. That holds for a row left dirty by an
earlier failure too, for as long as the lock is held.

Whether the lock is held is read with IS_USED_LOCK on MySQL and from
pg_locks on PostgreSQL. A failure to read it is returned, after `read
migration lock: `. Once the lock is free, dirty rows are `ErrDirty`
again. On SQLite a dirty row is always `ErrDirty`: SQLite never writes
a row ahead of a migration.

## Applied migrations

`Service.Applied` returns the rows of the tracking table as
`AppliedMigration` values, in version order: version, owner service,
description, dirty mark and checksum, empty for a row written without
one.

## Lifecycle

`Service.Init` runs these steps, in order:

1. It checks the lock timeout: `ErrInvalidLockTimeout` unless positive.
2. It picks the dialect from the provider's Driver.
3. It takes the provider's DB. A provider whose DB returns nil is
`ErrNilDatabase`.
4. Under the lock, it creates the tracking table when it does not
exist, or adds its checksum column when it lacks it, as described
under Tracking table.
5. It logs `service initialized` at info level with `dialect`.

The Driver names it knows:

- `postgres` and `pgx`: PostgreSQL.
- `mysql`: MySQL.
- `sqlite` and `sqlite3`: SQLite.

gas/database reports `postgres`, `mysql` and `sqlite`. Any other name
is `ErrUnsupportedDriver`, which lists the supported names and not the
one given.

Init on an initialized service returns `ErrAlreadyInitialized` and
changes nothing. A failed Init leaves the service uninitialized.

`Service.Close` makes the service uninitialized and logs `service
closed` at info level; it holds no connection to close. Init may run
again after it, and takes the provider's DB afresh, so restarting the
database means restarting migrate too. The registrations stay.

Until Init succeeds, after a failed Init and after Close, RunPending,
Down, ClearDirty, CheckReady and Applied return `ErrNotInitialized`.
`New`'s constructor fails with `ErrNilDatabase` for a nil provider and
`ErrNilLogger` for a nil logger.

## Tracking table

The table `TableName`, `__gas_migrations`, holds one row per applied or
dirty version:

- `version`: the primary key. On MySQL a VARCHAR(255) with the
database's collation, which by default ignores case, so `v1` and
`V1` are one version there.
- `service`: the owner service's name.
- `description`: the migration's description.
- `migrate_version` and `module_version`: unused, written empty. They
stay because tables of earlier releases have them.
- `applied_at`: when the row was written, set by the database. The
service never reads it, so the MySQL driver's parseTime does not
matter.
- `dirty`: whether the migration is marked dirty.
- `checksum`: the checksum of the up SQL the row was written for, or
empty, as described under Checksums. TEXT on PostgreSQL and SQLite,
VARCHAR(64) on MySQL.

Init creates the table with CREATE TABLE IF NOT EXISTS. When the table
exists without the `checksum` column, Init then adds it, under the
lock, with ALTER TABLE ... ADD COLUMN, NOT NULL with an empty default,
and logs `checksum column added` at info level. It looks for the column
in the schema the unqualified table name reaches: current_schema() on
PostgreSQL, DATABASE() on MySQL.

On PostgreSQL and MySQL the ALTER's wait for the table's lock is first
bounded to the lock timeout or 5 seconds, whichever is shorter:

- On PostgreSQL with lock_timeout, in whole milliseconds rounded up.
- On MySQL with lock_wait_timeout, in whole seconds rounded up.

The setting dies with the call's connection. While PostgreSQL waits for
that lock, later queries on the table queue behind it, which is why the
wait is short.

A failure fails Init, after `add checksum column: `. An ALTER that
waited out the bound fails with the driver's lock timeout error, not
`ErrLockTimeout`. So on an existing table Init needs the right to alter
it: on PostgreSQL, its ownership.

## Checksums

The checksum is the SHA-256, in lowercase hex, of the up SQL with every
CRLF replaced by LF, and nothing else changed, so that a checkout with
Windows line ends records the same checksum. A lone CR counts.

It covers the whole up SQL, its directives included, and nothing else:
editing the down SQL or the description after a migration was applied
is allowed. A migration registered with different SQL per dialect has a
checksum per dialect.

Every row the service inserts, dirty or clean, holds its migration's
checksum. An existing row's checksum is written only by RunPending's
recording of an empty one.

## Upgrading from v0.5

The tracking table of earlier releases is upgraded, not replaced: Init
adds the `checksum` column, as described under Tracking table, and the
rows, `applied_at` included, are read as they were written.

v0.5 names its columns in every statement. So instances of v0.5 keep
working on the upgraded table while instances of this release roll out,
the rows they write get an empty checksum, and going back to v0.5 keeps
working.

v0.5 takes no lock in the database, so the migration lock does not keep
it from the table while the column is added; the bound on the ALTER's
wait does.

The first RunPending records the checksums of the rows written without
one, from the up SQL registered at that time, and from then on checks
them. An edit made before that run is therefore not caught.

v0.5 read no directives. A migration whose up or down SQL opens with a
comment such as `-- gas: create users` now makes RunPending, Down and
CheckReady fail with `ErrUnknownDirective`, as described under
Directives. Reword the comment, as `-- create users`, in the build that
upgrades: its first RunPending records the reworded up SQL's checksum
for a row v0.5 wrote, so the edit is no `ErrChecksumMismatch`.

What changed besides is the order of versions:

- All-digit versions are ordered by their value, where v0.5 ordered
every version by its text. A set whose all-digit versions all have
the same number of digits, such as timestamps, keeps its order.
- Down(n) reverts the n highest versions in the new order, so with
all-digit versions of different lengths, such as `9` and `10`, it
may revert a different version than v0.5 would.
- On a fresh database, such versions are applied in the new order: `9`
before `10`, where v0.5 applied `10` first.
- Registering all-digit versions beside other versions, which v0.5
ordered by text, is now `ErrInvalidVersion`, reported by RunPending,
Down and CheckReady, so such an app fails to start until its
versions are renamed. Tracking tables that already mix them are
read, with the all-digit versions first, as under Versions.

## Edited migrations

RunPending fails with `ErrChecksumMismatch` while the up SQL registered
for an applied version is not the one its row records, and applies
nothing. The error names each such version, its owner and both
checksums, as listed under Errors. There is no option to accept an
edit. What to do:

1. Never edit a migration once it is applied anywhere: write a new
migration that makes the change. That holds for gas's own modules
too: a module never edits a migration it has shipped.
2. In development, for the latest migration: Down(1), then
RunPending. Down reverts it with its down SQL, which is not part of
the checksum, and deletes its row; RunPending applies the edited up
SQL and records its checksum.
3. As a last resort, by hand: `UPDATE __gas_migrations SET checksum =
'<registered>' WHERE version = '<version>'`, with the registered
checksum the error names. Run it before rolling out the edited
build: instances of the previous build fail RunPending on their next
start until they are rolled forward, and rolling back to the
previous build needs the UPDATE undone with the stored checksum the
error names.

## Errors

The sentinel errors, to be tested with `errors.Is`:

- `ErrNilDatabase`: New's constructor got a nil provider, or Init got
a nil DB from it.
- `ErrNilLogger`: New's constructor got a nil logger.
- `ErrNotInitialized`, `ErrAlreadyInitialized`: as under Lifecycle.
- `ErrUnsupportedDriver`, `ErrInvalidLockTimeout`: Init could not
start.
- `ErrNilService`, `ErrNilFS`, `ErrInvalidVersion`,
`ErrDuplicateVersion`, `ErrUnknownDirective`,
`ErrUnsupportedDirective`, `ErrInvalidMigrationFile`: a
registration failed, as under Registration and Migration files.
- `ErrDirty`: a migration is dirty; on MySQL, or with
`-- gas:no-transaction`, its changes may be partly applied and need
repair by hand before ClearDirty.
- `ErrTrackingRowLost`: a tracking row changed from outside the
service while a migration ran, as under Lost tracking rows.
- `ErrNotDirty`: ClearDirty got a version that is not dirty.
- `ErrPending`: CheckReady found migrations to apply.
- `ErrUnregisteredVersion`, `ErrNoDownSQL`: Down cannot revert a
version.
- `ErrMigrationFailed`: a migration's SQL, or writing its row, failed
once it had started. It wraps the driver's error. A failure before
anything ran, such as taking SQLite's write lock or writing the
dirty mark that comes first on MySQL, does not wrap it.
- `ErrLockTimeout`: the lock was not taken within the lock timeout.
- `ErrChecksumMismatch`: an applied migration's up SQL was edited, as
under Edited migrations.

Every error the package returns starts with `ServiceName` once. It
names what failed, a version with its owner when there is one, and
wraps the sentinel and then the cause:

```go
apply 002 (owner gas/auth/session): migration failed: <driver error>
```

Two failures carry a prefix of their own, followed by the driver's
error, or on SQLite by a failure of the write transaction, such as
`acquire lock: lock timed out`:

- A failure to add the checksum column fails Init after `add checksum
column: `.
- A failure to record a row's checksum fails RunPending after `record
checksum <version> (owner <owner>): `.

The errors never hold a migration's SQL, apart from what the driver's
own text quotes of it, such as the token a syntax error is near. The
driver's errors are wrapped so that an operator sees why a migration
failed.

When the provider has a method `SafeError(error) error`, as
gas/database's Service has, every driver error passes through it first,
so that a connection-level failure is replaced by one that holds no
user, host or database name. With another provider, driver errors are
wrapped as the driver returned them.

The errors, as returned, with SQLite's text for the driver errors:

```go
gas/migrate: database is nil
gas/migrate: logger is nil
gas/migrate: service not initialized
gas/migrate: service already initialized
gas/migrate: unsupported driver: supported are postgres, pgx, mysql, sqlite and sqlite3
gas/migrate: invalid lock timeout: must be positive
gas/migrate: register 001: service is nil
gas/migrate: register: service is nil
gas/migrate: register (owner app): invalid version: empty
gas/migrate: register v1 (owner app): invalid version: all-digit and other versions are mixed
gas/migrate: register 001 (owner billing): duplicate version: also registered by app
gas/migrate: register 001 (owner app): duplicate version: registered again with different content
gas/migrate: register 001 (owner app): up SQL: unknown directive on line 1
gas/migrate: register 001 (owner app): down SQL: unknown directive on line 2
gas/migrate: register 001 (owner app): up SQL: unsupported directive: no-transaction on sqlite
gas/migrate: register files: service is nil
gas/migrate: register files (owner app): file system is nil
gas/migrate: register files (owner app): invalid migration file: _x.up.sql has an empty version
gas/migrate: register files (owner app): invalid migration file: b/001_y.up.sql repeats the version of a/001_x.up.sql
gas/migrate: register files (owner app): invalid migration file: b/001_x.down.sql repeats a/001_x.down.sql
gas/migrate: register files (owner app): invalid migration file: 001_x.up.sql has no down file
gas/migrate: register files (owner app): invalid migration file: 001_x.down.sql has no up file
gas/migrate: register files (owner app): read files: open .: file does not exist
gas/migrate: acquire lock: lock timed out
gas/migrate: apply 001 (owner app): acquire lock: lock timed out
gas/migrate: acquire lock: context canceled
gas/migrate: dirty migrations: 001, 002
gas/migrate: pending migrations: 2
gas/migrate: checksum mismatch: 001 (owner app) stored e004ebd5b5532a4b85984a62f8ad48a81aa3460c1ca07701f386135d72cdecf5 registered ebbb5b332060a3ede7047bd7528883b0a560e729848f9feb8ff742145e909b01
gas/migrate: apply 001 (owner app): migration failed: SQL logic error: no such table: missing (1)
gas/migrate: revert 001 (owner app): migration failed: SQL logic error: no such table: missing (1)
gas/migrate: revert 001 (owner gone): version not registered
gas/migrate: revert 001 (owner app): no down SQL
gas/migrate: clear 001: migration not dirty
```

## API

Run `go doc -all github.com/gasmod/gas/migrate` for every declaration, or `go doc github.com/gasmod/gas/migrate.<Name>` for one.
