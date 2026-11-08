// Package database connects a Gas app to PostgreSQL, MySQL or SQLite and
// provides the connection as a gas.DatabaseProvider. In [ModeSQL] it opens
// a database/sql handle with the dialect's driver, or a connector given to
// [WithConnector]. In [ModePgx] it opens a native pgxpool.Pool for
// PostgreSQL and derives the database/sql handle from it. [Service]
// implements gas.Service, gas.DatabaseProvider, gas.HealthReporter and
// gas.ReadyReporter.
//
// # Usage
//
// In an app, the container builds the service from [New] and calls its
// Init. By hand, with a configuration given in code:
//
//	cfg := database.DefaultConfig()
//	cfg.Database.Dialect = database.DialectSQLite
//	cfg.Database.DSN = "file:app.db"
//
//	svc, err := database.New(database.WithConfig(cfg))(nil, logger)
//	if err != nil {
//		return err
//	}
//	if err := svc.Init(ctx); err != nil {
//		return err
//	}
//	defer svc.Close(ctx)
//
//	err = svc.WithTx(ctx, nil, func(tx *sql.Tx) error {
//		_, err := tx.ExecContext(ctx, "INSERT INTO notes (body) VALUES (?)", "hello")
//		return err
//	})
//
// The app imports modernc.org/sqlite for the `sqlite` driver. The insert
// is committed, since the function returned nil, and svc.Driver() is
// `sqlite`.
//
// # Configuration
//
// The settings live in the `database` section, in [Settings]:
//
//   - `database.mode`: [ModeSQL] or [ModePgx]. Default: [DefaultMode].
//   - `database.dialect`: [DialectPostgres], [DialectMySQL] or
//     [DialectSQLite]. It has no default, and is required unless a
//     connector is given.
//   - `database.dsn`: the connection string, in the format of the
//     dialect's driver, a gas.Secret, which fmt, encoding/json, YAML
//     encoders and log/slog write as `[redacted]`. It has no default, and
//     is required unless a connector is given.
//   - `database.maxopenconns`: the most open connections, 1 to 1000.
//     Default: [DefaultMaxOpenConns].
//   - `database.maxidleconns`: the most idle connections, 0 to
//     `maxopenconns`. Default: [DefaultMaxIdleConns]. ModeSQL applies it;
//     ModePgx has no such setting and checks it without applying it.
//   - `database.connmaxlifetime`: the longest time a connection is
//     reused, 1s to 2h. Default: [DefaultConnMaxLifetime].
//   - `database.connmaxidletime`: the longest time a connection stays
//     idle, 1s to 1h and at most `connmaxlifetime`. Default:
//     [DefaultConnMaxIdleTime].
//   - `database.connecttimeout`: the bound of each connect attempt, open
//     and ping, of each later ping, and of a pgx rollback, 1s to 1m.
//     Default: [DefaultConnectTimeout].
//   - `database.connretries`: how many times a failed connect is retried,
//     0 to 10. Default: [DefaultConnRetries].
//   - `database.connretryinterval`: the wait before the first retry,
//     100ms to [MaxConnRetryInterval] (30s), checked only when
//     `connretries` is positive. Default: [DefaultConnRetryInterval].
//
// Both modes apply every limit they have as configured; no zero stands
// for a driver default. In ModePgx the settings replace the `pool_*`
// parameters of the DSN that they match.
//
// [Service.Init] resolves the configuration each time it runs. Without
// [WithConfig], it binds [DefaultConfig] from the gas.ConfigProvider,
// which checks the `validate` tags; a nil ConfigProvider is the error
// [ErrNoConfigProvider]. With WithConfig, it takes a copy of the given
// Config, never changing the caller's struct, and checks the same tags; a
// nil Config is the error [ErrNilConfig]. Either way, these rules then
// run, in this order, and the first that fails is the error:
//
//  1. With a connector: WithConnector was given one, with one of the
//     three dialects; the mode is ModeSQL; the dialect setting is empty
//     or the connector's; the DSN is empty.
//  2. Without a connector: the dialect is set; the DSN is set; ModePgx
//     has the dialect postgres.
//  3. `maxidleconns` is at most `maxopenconns`.
//  4. When `connretries` is positive, `connretryinterval` is in bounds.
//
// A failure wraps [ErrInvalidConfig] and names the setting, never its
// value.
//
// [Service.Config] returns a copy of the configuration the last
// successful Init resolved, defaults included, with DSN still a
// gas.Secret, or the zero Config before one. A failed Init and Close
// leave it as it was. It holds the settings as resolved: the DSN without
// the busy timeout the dialect sqlite may add, and with a connector the
// Dialect setting as given, which may be empty; Driver reports the
// dialect in use.
//
// # Modes
//
// ModeSQL opens a *sql.DB and applies the four limits to it. ModePgx
// opens a *pgxpool.Pool, with `maxopenconns` as its MaxConns and the
// lifetime and idle time as its own; [Service.DB] is then derived from
// the pool with pgx's stdlib.OpenDBFromPool, keeps no idle connections
// of its own, and borrows the pool's. [Service.Pool] and [PoolFrom]
// return the pool in ModePgx, and nil in ModeSQL. ModePgx requires the
// dialect postgres and takes no connector.
//
// # Dialects
//
// The dialect picks the driver in ModeSQL, and is what [Service.Driver]
// returns, so that the services built on the database, such as migrate,
// pick their SQL by it:
//
//   - postgres: pgx's database/sql adapter, on the DSN parsed by
//     pgx.ParseConfig. The module imports it; no registration is needed.
//   - mysql: github.com/go-sql-driver/mysql, on the DSN parsed by
//     mysql.ParseDSN. The module imports it. ParseTime is always turned
//     on, so DATETIME and TIMESTAMP columns scan into time.Time.
//     MultiStatements stays as the DSN sets it, off unless it says
//     `multiStatements=true`, since it widens what an injected statement
//     can do. The driver's own log, which by default writes its messages
//     to stderr with network errors that name the server's address,
//     goes to the service's logger instead, as one warn event
//     `mysql driver reported an error` per message, without the
//     driver's text. A connector given to WithConnector keeps its own
//     logger.
//   - sqlite: the database/sql driver registered as `sqlite`, opened with
//     the DSN as it is, except for the busy timeout. The module does not
//     import one: the app imports modernc.org/sqlite, or another driver
//     registered under that name. Without one, Init fails with
//     [ErrDriverNotRegistered], naming the import. When the DSN sets no
//     busy timeout, the service adds [DefaultSQLiteBusyTimeout], 5
//     seconds, as modernc.org/sqlite reads it: `_pragma=busy_timeout(5000)`
//     is appended to the DSN's query. Without a busy timeout SQLite fails
//     at once, with SQLITE_BUSY, a statement that meets another
//     connection's lock, so two connections of one pool writing, or one
//     reading while another commits, fail; with it the statement waits up
//     to that long. A DSN sets one with `_busy_timeout`, `_timeout`, or a
//     `_pragma` value starting with `busy_timeout`, such as
//     `_pragma=busy_timeout(0)`, which turns the wait off. A DSN whose
//     query does not parse is left as it is, for the driver to reject.
//     Another driver registered as `sqlite` must accept that parameter.
//
// # Connectors
//
// [WithConnector] gives ModeSQL a driver.Connector to open the handle
// with, for what a DSN cannot express, such as a custom TLS setup or IAM
// authentication, and the dialect it speaks, which Driver returns. The
// connector is reused by every connect attempt and every Init, and never
// closed by the service: closing the handle does not reach it. The
// caller owns it.
//
// # Connecting
//
// Init first does what fails the same way every time: it parses the DSN
// and finds the driver. A DSN the driver cannot parse is [ErrInvalidDSN],
// and a missing driver ErrDriverNotRegistered; neither is retried. It
// then makes connect attempts. Each attempt opens the handle, or the
// pool, and pings it, within Init's context and `connecttimeout`, and
// closes what it opened when the ping fails.
//
// A failed attempt is retried up to `connretries` times. Before each
// retry, Init logs `database connection failed, retrying` at warn level
// with `attempt` (from 1), `retries`, `wait` and `reason`, and waits:
// `connretryinterval` before the first retry, then twice the previous
// wait, up to [MaxConnRetryInterval]. The wait ends early when Init's
// context ends, and Init then fails at once.
//
// When the attempts run out, Init fails with an error wrapping
// [ErrConnect], `connect: ` and a reason, and, when Init's context ended,
// the context's error too. The reason is the first of these that holds
// for the driver's error:
//
//   - `authentication failed`: PostgreSQL's SQLSTATE 28P01 or 28000, or
//     MySQL's error 1045.
//   - `access denied`: MySQL's error 1044, the user denied the database.
//     MySQL reports it for a database that does not exist too, unless
//     the user has privileges on every database.
//   - `database does not exist`: PostgreSQL's 3D000, or MySQL's 1049.
//   - `timed out`: a deadline passed, in a context or on the network.
//   - `connection refused`: the server refused the TCP connection.
//   - `unreachable`: any other network error, such as a failed name
//     lookup.
//   - `failed`: anything else.
//
// The driver's error itself is not kept: pgx's and MySQL's connect errors
// can hold the user, the host, the database and, in pgx's
// ParseConfigError and ConnectError, the password and the TLS
// configuration. The reason and the database server's own logs are what
// an operator diagnoses with.
//
// The same holds after Init. The handle and the pool open connections
// whenever they need one, after a server restart, a credential rotation
// or when `connmaxlifetime` ends one, and the call that needed it gets
// the failure. So Query, Exec, BeginTx, BeginPgxTx, WithTx, WithPgxTx,
// their commit and rollback, and Close replace a connection-level
// failure with `<op>: <reason>`, `<op>` as in the error's usual text and
// the reason as above, wrapping ErrConnect and, when the call's context
// ended, the context's error, never the driver's. A failure is
// connection-level when it comes from opening a connection with the
// dialect postgres or mysql or with a connector (in pgx, a
// *pgconn.ConnectError); a sqlite driver's errors are not told apart
// that way. It is also connection-level, unless a context ended the
// call, when it is a network error (a net.Error), driver.ErrBadConn,
// MySQL's mysql.ErrInvalidConn or pgx's pgconn.ErrConnClosed. Calls made on
// [Service.DB] or [Service.Pool] directly get the driver's errors as
// they are.
//
// [Service.SafeError] gives code that runs statements on DB itself, such
// as migrate, the same replacement: it returns a connection-level failure
// as `connection: ` and a reason, wrapping ErrConnect and not the
// driver's error, and any other error unchanged. Having no context, it
// wraps no context error. It works in any state of the service.
//
// # Lifecycle
//
// A successful Init publishes the handle, logs `service initialized` at
// info level with `mode` and `dialect`, and only then do the methods
// below use it. Init on an initialized service returns
// [ErrAlreadyInitialized] and changes nothing: the running handle stays
// open. A failed Init closes what it opened and leaves the service
// uninitialized.
//
// [Service.Close] makes the service uninitialized, then closes the
// handle and, in ModePgx, the pool, which waits for the connections the
// caller holds, such as a transaction from BeginPgxTx, to be released.
// The pool is closed even when closing the handle fails, and that
// failure is returned. When the closing finishes, Close returns, having
// logged `service closed` at info level if it succeeded and nothing if
// it failed. When Close's context ends first, Close returns an error
// wrapping [ErrCloseTimeout] and the context's error, and the closing
// goes on: when it finishes, it logs `service closed` at info level, or
// `service close failed` at warn level with `reason`. Close on a service
// that is not initialized returns nil and logs nothing. After Close,
// even one that timed out, Init may run again, and resolves the
// configuration afresh.
//
// Restarting the service makes a new handle; the old one is closed. The
// services that take [Service.DB] in their own Init, such as migrate and
// the auth stores, keep the old one, so restarting the database means
// restarting them too.
//
// Until Init succeeds, after a failed Init and after Close, DB and Pool
// return nil, PoolFrom returns nil and false, Driver returns an empty
// string, and [Service.Ping], [Service.Query], [Service.Exec],
// [Service.BeginTx], [Service.WithTx], [Service.BeginPgxTx],
// [Service.WithPgxTx], [Service.CheckHealth] and [Service.CheckReady]
// return an error wrapping [ErrNotInitialized]. [New]'s constructor fails
// with [ErrNilLogger] for a nil logger; a nil ConfigProvider is accepted,
// and Init then requires WithConfig.
//
// # Transactions
//
// [Service.WithTx] begins a transaction on DB, runs the function with it,
// and commits only when the function returned nil. When it returned an
// error, panicked or called runtime.Goexit, as t.FailNow does, the
// transaction is rolled back. A panic or Goexit then goes on, and a
// failed rollback is logged at warn level as `transaction rollback
// failed`, with `reason`, a reason as for a failed connect, and not the
// driver's error. After an error return, WithTx returns the function's
// error unchanged, joined with the rollback's error when the rollback
// failed. A rollback that finds the transaction already ended is not a
// failure. A function that commits or rolls back the transaction itself
// and returns nil gets an error wrapping sql.ErrTxDone from the commit.
// database/sql takes no context for a rollback, and rolls the
// transaction back itself when the context of WithTx ends.
//
// [Service.WithPgxTx] is the same for a pgx.Tx on the pool, in ModePgx
// only; elsewhere it returns [ErrModeNotPgx]. It commits with its
// context, so a context that has ended fails the commit rather than
// persisting work the caller gave up on. It rolls back with that context
// without its cancellation, bounded by `connecttimeout`, so the rollback
// runs when the context has ended and cannot hang on a dead connection.
// A function that ends the transaction itself and returns nil gets an
// error wrapping pgx.ErrTxClosed. A nil function is [ErrNilFunc] for
// both, checked before the transaction begins.
//
// [Service.BeginTx] and [Service.BeginPgxTx] begin a transaction that
// the caller ends. Errors from the driver in Query, Exec and the
// transaction methods that describe the statement come back wrapped;
// connection-level failures are replaced, as described under Connecting.
//
// # Health
//
// [Service.CheckHealth] does no I/O: it fails only when the service is
// not initialized, which a restart fixes. [Service.CheckReady] and
// [Service.Ping] ping the database, within their context and
// `connecttimeout`. A failed ping wraps ErrConnect, after `ping: `, with
// a reason as a failed connect has, and the context's error when the
// caller's context ended.
//
// # Concurrency
//
// A Service is safe for concurrent use. Init and Close run one at a time:
// a Close waits for a running Init, connect retries included, and then
// closes what it opened. Close holds its lock only to make the service
// uninitialized, so an Init can run while an earlier Close is still
// closing. Init swaps in what it opened at once, so the other methods,
// which take no lock, use one Init's handle throughout a call. A call
// that runs beside Close may find its handle closed and return the
// driver's error. Close closes in a goroutine of its own, which outlives
// a Close whose context ended until the closing finishes; the service
// starts no other goroutines. In ModePgx, the pool runs pgx's health
// check, and opens connections up to its minimum, until it is closed.
//
// # Errors
//
// The sentinel errors, to be tested with [errors.Is]:
//
//   - [ErrNilLogger]: New's constructor got a nil logger.
//   - [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig]: Init
//     could not resolve a valid configuration. ErrInvalidConfig wraps the
//     binder's, the validator's or the rule's error.
//   - [ErrAlreadyInitialized]: Init ran on an initialized service.
//   - [ErrDriverNotRegistered]: no `sqlite` driver is registered.
//   - [ErrInvalidDSN]: the dialect's driver could not parse the DSN.
//   - [ErrConnect]: connecting or pinging failed, or a call after Init
//     failed at the connection level, or SafeError replaced such a
//     failure, as described under Connecting.
//   - [ErrNotInitialized]: a method ran before Init succeeded, after a
//     failed Init, or after Close.
//   - [ErrModeNotPgx]: a pgx transaction was asked for in ModeSQL.
//   - [ErrNilFunc]: WithTx or WithPgxTx got a nil function.
//   - [ErrCloseTimeout]: Close's context ended before the closing
//     finished. It wraps the context's error too.
//
// Every error the package makes starts with [ServiceName] and wraps its
// sentinel. The errors that pass a statement's failure on wrap no
// sentinel; they wrap the driver's error, after a prefix:
//
//   - Query: `gas/database: query: `.
//   - Exec: `gas/database: exec: `.
//   - BeginTx, BeginPgxTx, WithTx and WithPgxTx:
//     `gas/database: failed to begin transaction: `.
//   - WithTx and WithPgxTx:
//     `gas/database: failed to commit transaction: `.
//   - WithTx and WithPgxTx, after the function returned an error:
//     `gas/database: failed to roll back transaction: `, joined after
//     the function's error.
//   - Close: `gas/database: close: `.
//
// When the function given to WithTx or WithPgxTx returns an error and the
// rollback succeeds, they return that error unchanged: not wrapped, and
// without the prefix.
//
// No error the package makes and no log the service writes includes the
// DSN, the password, the user, the host, the port, the database name or
// TLS material; errors name the setting instead. The logs carry the
// mode, the dialect, the retry `attempt`, `retries` and `wait`, and fixed
// reasons. The driver's errors that Query, Exec, the transaction methods
// and Close pass on are the driver's own text: they describe the
// statement, and can name what it touched, such as a table or, in an
// access error, a database or the user.
//
// A configuration that fails its tags gets the validator's error after
// `gas/database: invalid config: validate config: `, such as:
//
//	Key: 'Config.Database.Mode' Error:Field validation for 'Mode' failed on the 'oneof' tag
//
// The other errors, as returned:
//
//	gas/database: logger is nil
//	gas/database: config is nil
//	gas/database: no config provider
//	gas/database: invalid config: WithConnector requires a connector
//	gas/database: invalid config: WithConnector requires the dialect postgres, mysql or sqlite
//	gas/database: invalid config: a connector requires Database.Mode sql
//	gas/database: invalid config: with a connector, Database.Dialect must be empty or the connector's dialect
//	gas/database: invalid config: with a connector, Database.DSN must be empty
//	gas/database: invalid config: Database.Dialect is required without a connector
//	gas/database: invalid config: Database.DSN is required without a connector
//	gas/database: invalid config: Database.Mode pgx requires Database.Dialect postgres
//	gas/database: invalid config: Database.MaxIdleConns must not exceed Database.MaxOpenConns
//	gas/database: invalid config: Database.ConnRetryInterval must be 100ms to 30s when Database.ConnRetries is positive
//	gas/database: service already initialized
//	gas/database: driver not registered: dialect sqlite needs the import modernc.org/sqlite
//	gas/database: invalid DSN
//	gas/database: connect: authentication failed
//	gas/database: connect: access denied
//	gas/database: connect: database does not exist
//	gas/database: connect: timed out
//	gas/database: connect: connection refused
//	gas/database: connect: unreachable
//	gas/database: connect: failed
//	gas/database: connect: failed: context canceled
//	gas/database: ping: connection refused
//	gas/database: query: connection refused
//	gas/database: exec: connection refused
//	gas/database: failed to begin transaction: authentication failed
//	gas/database: failed to commit transaction: connection refused
//	gas/database: failed to roll back transaction: connection refused
//	gas/database: close: connection refused
//	gas/database: connection: connection refused
//	gas/database: close timed out: context deadline exceeded
//	gas/database: service not initialized
//	gas/database: mode is not pgx
//	gas/database: transaction function is nil
//	gas/database: failed to commit transaction: sql: transaction has already been committed or rolled back
package database
