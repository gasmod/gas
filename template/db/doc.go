// Package db keeps templates in a table of a PostgreSQL, MySQL or SQLite
// database, reached through a gas.DatabaseProvider such as gas/database's
// Service, with the table created by migrations that a
// gas.MigrationManager, such as gas/migrate's Service, applies. [Store]
// implements gas.TemplateProvider with the contract of the template
// package, and gas.Service. It is the store for templates edited while
// the app runs, by trusted administrators only, as the template package's
// Security section describes.
//
// # Usage
//
// In an app, register the constructor; it takes the database, the logger
// and the migration manager from the container:
//
//	gas.WithSingletonService[gas.TemplateProvider](db.NewStore(db.WithNamespace("emails")))
//
// By hand:
//
//	store, err := db.NewStore()(database, logger, migrations)
//	if err != nil {
//		return err
//	}
//	if err := store.Init(ctx); err != nil {
//		return err
//	}
//	defer store.Close(ctx)
//	if err := migrations.RunPendingContext(ctx); err != nil {
//		return err
//	}
//
//	err = store.Register(ctx, "welcome.html", []byte("<p>Hi {{.Name}}</p>"))
//
// After this the table `__gas_templates` exists and holds one row,
// namespace `default`, name `welcome.html`.
//
// # Table
//
// Every Store keeps its templates in [TableName], `__gas_templates`, one
// row per template: `namespace`, `name` and `content`, unique on
// namespace and name, with `id`, `created_at` and `updated_at`, which the
// store writes but never reads. A Store reads and writes only the rows of
// its namespace, [DefaultNamespace] unless [WithNamespace] gives another,
// so several stores share the table, each its own set of templates. A
// namespace is valid UTF-8 without a NUL byte, of 1 to 255 characters;
// the constructor returns an error wrapping [ErrInvalidNamespace] for
// another, which gives the reason and never the namespace. A name is at
// most 255 characters too, on every dialect: a longer one is refused
// with template.ErrInvalidName before any statement runs. MySQL's columns
// hold no more, and on PostgreSQL a row of the unique index on namespace
// and name must fit in 2704 bytes, which 255 characters of four bytes
// each, of both, do.
//
// Get selects the row of the name, with its content only when that is
// within the store's maximum size, [WithMaxSize], so a larger row is
// template.ErrTooLarge without the database sending it; List selects the
// names of the namespace,
// Register inserts the row or replaces its content, in one statement, and
// RegisterFS does the same for each file in one transaction, which a
// failure rolls back, so it registers all or none. [Store.Exists] reports
// whether a row is there, and [Store.Delete] deletes it, or returns an
// error wrapping template.ErrTemplateNotFound when there is none. List
// sorts the names in Go, in byte order, whatever the database's
// collation, and leaves out a row whose name the store would refuse, such
// as one another store of the namespace wrote with other extensions.
//
// # Dialects
//
// Init picks the SQL from the provider's Driver: `postgres`, `mysql` or
// `sqlite`, the names gas/database reports. Any other is
// [ErrUnsupportedDriver], which lists the supported names and not the one
// given; so is gas/database before its own Init, which reports an empty
// driver. MySQL must be 8.0.19 or later, for the `AS new` alias the
// upsert uses and the utf8mb4_0900_bin collation; MariaDB has neither and
// is not supported. Names are exact on every dialect:
//
//   - PostgreSQL: TEXT names and BYTEA content, compared byte for byte.
//   - SQLite: TEXT names and BLOB content, compared byte for byte by the
//     BINARY collation.
//   - MySQL: `namespace` and `name` are VARCHAR(255) of utf8mb4 with the
//     utf8mb4_0900_bin collation, which compares code points and pads
//     nothing, so `Home.html` and `home.html`, and `café.html` and
//     `cafe.html`, are different names. Content is LONGBLOB, which holds any
//     bytes as given, without MEDIUMBLOB's 16 MiB cap; a single Register
//     is limited by the server's max_allowed_packet, 64 MiB by default on
//     MySQL 8.4.
//
// Content is otherwise limited only by the store's maximum size, 16 MiB
// by default, and the largest value the database stores.
//
// # Migrations
//
// Init registers two migrations with the migration manager, owned by the
// store under [ServiceName], the same for every dialect but in its own
// SQL. Every store registers the same two, so several stores on one
// manager register them once; the manager applies them when it runs, and
// until then Register and the other methods fail with [ErrQuery] and the
// driver's code for a missing table. Seed templates once the
// migrations ran, such as in a ready function of the app.
//
//   - `20260322235959`, `create templates table`: creates the table, with
//     an index on `namespace`. On MySQL it creates `name` and `namespace`
//     with the database's default collation and `content` as MEDIUMBLOB.
//     Its SQL is unchanged from earlier releases, so that a database where
//     it was applied keeps its row.
//   - `20261031120000`, `exact template names`: drops the index on
//     `namespace`, which the unique key on namespace and name already
//     serves, on every dialect. On MySQL it also changes `namespace` and
//     `name` to the utf8mb4_0900_bin collation and `content` to LONGBLOB,
//     in one ALTER TABLE that keeps every row. A table of an earlier
//     release cannot hold two names that the new collation tells apart
//     and the old one did not, so the unique key holds through the change.
//     On MySQL both directions first add STRICT_ALL_TABLES to the
//     session's sql_mode, so that a value that does not fit fails the
//     ALTER TABLE, which then changes nothing, instead of being cut short
//     or replaced by `?` with a warning, whatever the server's sql_mode.
//     gas/migrate runs a migration on a connection of its own and closes
//     it after, so the setting ends with it.
//
// Reverting `20261031120000` restores the index, and on MySQL the
// database's default collation and MEDIUMBLOB. On MySQL the revert fails,
// and leaves the migration dirty, when two names of a namespace differ
// only in case or accents, which the default collation takes as one, when
// a name or namespace holds a character the database's default
// character set cannot, such as `日本.html` in a latin1 database, or when
// a template's content is over 16 MiB, which MEDIUMBLOB cannot hold; the
// table is then left as it was. Delete such rows before reverting.
//
// # Lifecycle
//
// [NewStore]'s constructor checks its dependencies and options and touches
// no database. Init picks the dialect, takes the provider's DB, and
// registers the migrations; it runs no statement. It returns
// [ErrNilDatabase] when the provider's DB is nil, and changes nothing on
// failure. Init on an open store returns template.ErrAlreadyInitialized.
// Close makes the store closed and closes nothing, since the database is
// the provider's; Init may run again after it, and takes the provider's
// DB afresh, so restarting the database means restarting the store too.
// Before the first successful Init every method but Init and Close
// returns template.ErrNotInitialized, and after Close, until Init runs
// again, template.ErrClosed. Init logs `store initialized` at info level,
// with `dialect` and `namespace`.
//
// The gas container builds one service per registered type, so a second
// Store, in another namespace, needs a type of its own, such as one
// registered as gas.TemplateProvider and the other as *db.Store. The gas
// worker tracks services by instance and closes both at shutdown, but as
// every Store is named ServiceName, and no option gives it another name,
// its CloseService and RestartService refuse either with
// gas.ErrDuplicateServiceName.
//
// # Concurrency
//
// A Store is safe for concurrent use. A read-write lock guards its state,
// held for reading only while a method takes the handle, never while a
// statement runs. Statements run on the provider's *sql.DB pool, and two
// Registers of one name at once leave one of the two contents.
//
// # Errors
//
// The sentinels this package adds, to be tested with [errors.Is]:
//
//   - [ErrNilDatabase], [ErrNilLogger], [ErrNilMigrations]: the
//     constructor was given a nil dependency, or Init got a nil DB.
//   - [ErrInvalidNamespace]: WithNamespace was given a namespace the store
//     refuses.
//   - [ErrUnsupportedDriver]: the provider reports a driver the store has
//     no SQL for.
//   - [ErrQuery]: a statement failed. It never wraps the driver's error.
//
// The methods return errors wrapping the template package's sentinels
// too: ErrInvalidName, ErrTemplateNotFound, ErrTooLarge,
// ErrInvalidExtension, ErrInvalidMaxSize, ErrNilFS, ErrRead and the
// context's error from RegisterFS, ErrNotInitialized,
// ErrAlreadyInitialized and ErrClosed.
//
// A driver's error text can hold the database's user, the client's
// address and the database's name, as MySQL's 1142, `SELECT command
// denied to user`, and 1146, `Table 'app.__gas_templates' doesn't
// exist`, do. So an ErrQuery error names the operation and the template,
// and then gives no driver text:
//
//   - When the provider has a method `SafeError(error) error`, as
//     gas/database's Service has, the driver's error passes through it
//     first, and a connection-level failure, which it replaces with its
//     own fixed text, keeps that text and what it wraps, such as
//     gas/database's ErrConnect.
//   - A context that ended, or database/sql's ErrConnDone or ErrTxDone,
//     is kept, wrapped.
//   - Any other error gives only its code: `mysql error` and MySQL's
//     error number, `postgres error` and PostgreSQL's SQLSTATE, `sqlite
//     error` and SQLite's result code, or `driver error` for an error of
//     another driver. The code is enough to look the failure up, and the
//     database's own log holds the rest.
//
// The errors of this package, as returned, with SQLite's codes:
//
//	template: database is nil
//	template: logger is nil
//	template: migration manager is nil
//	template: invalid namespace: empty
//	template: invalid namespace: not valid UTF-8
//	template: invalid namespace: holds a NUL byte
//	template: invalid namespace: longer than 255 characters
//	template: unsupported driver: supported are postgres, mysql and sqlite
//	template: invalid name: longer than 255 characters
//	template: query failed: get "welcome.html": sqlite error 1
//	template: query failed: get "welcome.html": context canceled
//	template: query failed: list: sqlite error 1
//	template: query failed: register "welcome.html": sqlite error 1
//	template: query failed: exists "welcome.html": sqlite error 1
//	template: query failed: delete "welcome.html": sqlite error 1
package db
