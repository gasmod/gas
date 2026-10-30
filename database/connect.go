package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// sqliteDriverName is the database/sql driver name DialectSQLite opens.
const sqliteDriverName = "sqlite"

// sqliteImport is the package that registers sqliteDriverName.
const sqliteImport = "modernc.org/sqlite"

// backoffFactor is what each wait between connect retries multiplies the
// one before by.
const backoffFactor = 2

// opener opens the database once: one connect attempt, before its ping.
// A returned error wrapping ErrInvalidDSN is permanent and is not
// retried; any other fails the attempt.
type opener func(ctx context.Context) (*sql.DB, *pgxpool.Pool, error)

// connect opens the database the settings describe, retrying failed
// attempts as the settings say, and returns the state to publish.
func (s *Service) connect(ctx context.Context, set *Settings) (*state, error) {
	dialect := set.Dialect
	if s.connectorSpec.set {
		dialect = s.connectorSpec.dialect
	}

	open, err := s.prepare(set, dialect)
	if err != nil {
		return nil, err
	}

	db, pool, err := s.retry(ctx, set, open)
	if err != nil {
		return nil, err
	}

	return &state{db: db, pool: pool, dialect: dialect, settings: *set}, nil
}

// prepare does the work that fails the same way every time, parsing the
// DSN and finding the driver, and returns what opens the database. Its
// errors are permanent.
func (s *Service) prepare(set *Settings, dialect Dialect) (opener, error) {
	if s.connector != nil {
		return sqlOpener(set, phaseConnector{s.connector}), nil
	}

	if set.Mode == ModePgx {
		return pgxOpener(set)
	}

	//nolint:exhaustive // the rules allow no dialect but the three; sqlite is the default.
	switch dialect {
	case DialectPostgres:
		cc, err := pgx.ParseConfig(set.DSN)
		if err != nil {
			return nil, invalidDSN()
		}

		return sqlOpener(set, phaseConnector{stdlib.GetConnector(*cc)}), nil
	case DialectMySQL:
		c, err := mysqlConnector(set.DSN, s.logger)
		if err != nil {
			return nil, err
		}

		return sqlOpener(set, phaseConnector{c}), nil
	default: // DialectSQLite; the rules allow no other
		if !slices.Contains(sql.Drivers(), sqliteDriverName) {
			return nil, fmt.Errorf(
				"%s: %w: dialect %s needs the import %s",
				ServiceName, ErrDriverNotRegistered, DialectSQLite, sqliteImport,
			)
		}

		return sqliteOpener(set), nil
	}
}

// invalidDSN returns the error for a DSN the driver could not parse. The
// parser's error is not kept: it can quote the DSN.
func invalidDSN() error {
	return fmt.Errorf("%s: %w", ServiceName, ErrInvalidDSN)
}

// sqlOpener returns an opener of a database/sql handle on c with the
// settings' limits applied.
func sqlOpener(set *Settings, c driver.Connector) opener {
	return func(context.Context) (*sql.DB, *pgxpool.Pool, error) {
		db := sql.OpenDB(c)
		applyLimits(db, set)

		return db, nil, nil
	}
}

// sqliteOpener returns an opener of a database/sql handle on the
// registered sqlite driver with the settings' limits applied.
func sqliteOpener(set *Settings) opener {
	return func(context.Context) (*sql.DB, *pgxpool.Pool, error) {
		db, err := sql.Open(sqliteDriverName, set.DSN)
		if err != nil {
			return nil, nil, invalidDSN()
		}

		applyLimits(db, set)

		return db, nil, nil
	}
}

// applyLimits applies the settings' connection limits to db.
func applyLimits(db *sql.DB, set *Settings) {
	db.SetMaxOpenConns(int(set.MaxOpenConns))
	db.SetMaxIdleConns(set.MaxIdleConns)
	db.SetConnMaxLifetime(set.ConnMaxLifetime)
	db.SetConnMaxIdleTime(set.ConnMaxIdleTime)
}

// pgxOpener parses the DSN as a pool configuration with the settings'
// limits, and returns an opener of a pool on a copy of it.
func pgxOpener(set *Settings) (opener, error) {
	poolCfg, err := pgxpool.ParseConfig(set.DSN)
	if err != nil {
		return nil, invalidDSN()
	}

	poolCfg.MaxConns = set.MaxOpenConns
	poolCfg.MaxConnLifetime = set.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = set.ConnMaxIdleTime

	return func(ctx context.Context) (*sql.DB, *pgxpool.Pool, error) {
		// The pool keeps the context it is made with for the connections
		// it opens in the background up to its minimum, after the attempt
		// returns; it gets ctx without the attempt's timeout and
		// cancellation, and only the ping runs under them.
		pool, err := pgxpool.NewWithConfig(context.WithoutCancel(ctx), poolCfg.Copy())
		if err != nil {
			//nolint:wrapcheck // retry classifies the error and drops it.
			return nil, nil, err
		}

		return stdlib.OpenDBFromPool(pool), pool, nil
	}, nil
}

// phaseConnector is the connector a ModeSQL handle opens with. It marks
// every error of c's Connect as a phaseError, so that a failure to open a
// connection after Init, which database/sql returns from the call that
// needed it, is told from a statement's failure. It also hides the Close
// method of a connector given to WithConnector, so that closing a handle
// opened on it, after a failed attempt or in Close, leaves it usable for
// the next attempt and the next Init: the caller owns it.
type phaseConnector struct {
	// c is the connector wrapped.
	c driver.Connector
}

// Connect is c's Connect, with its error marked as a phaseError.
func (p phaseConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := p.c.Connect(ctx)
	if err != nil {
		return nil, &phaseError{err: err}
	}

	return conn, nil
}

// Driver is c's Driver.
func (p phaseConnector) Driver() driver.Driver { return p.c.Driver() }

// phaseError is an error of opening a connection. Its text is the
// driver's, which the service never returns.
type phaseError struct {
	// err is the driver's error.
	err error
}

// Error returns the driver's text.
func (e *phaseError) Error() string { return e.err.Error() }

// Unwrap returns the driver's error.
func (e *phaseError) Unwrap() error { return e.err }

// retry runs connect attempts until one succeeds, the retries run out, or
// ctx ends, waiting between them as the package documentation describes.
func (s *Service) retry(
	ctx context.Context,
	set *Settings,
	open opener,
) (*sql.DB, *pgxpool.Pool, error) {
	wait := set.ConnRetryInterval

	for attempt := 1; ; attempt++ {
		db, pool, err := attemptConnect(ctx, set, open)
		if err == nil {
			return db, pool, nil
		}

		if errors.Is(err, ErrInvalidDSN) {
			return nil, nil, err
		}

		if attempt > set.ConnRetries || ctx.Err() != nil {
			return nil, nil, connectError(ctx, "connect", err)
		}

		s.logger.Warn("database connection failed, retrying").
			Int("attempt", attempt).
			Int("retries", set.ConnRetries).
			Duration("wait", wait).
			Str("reason", reason(err)).
			Send()

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, nil, connectError(ctx, "connect", err)
		case <-timer.C:
		}

		wait = min(backoffFactor*wait, MaxConnRetryInterval)
	}
}

// attemptConnect opens the database and pings it, both within ctx and
// set.ConnectTimeout, and closes what it opened when the ping fails. An
// error wrapping ErrInvalidDSN is permanent; any other is the attempt's
// failure, joined with the error of closing what it opened.
func attemptConnect(
	ctx context.Context,
	set *Settings,
	open opener,
) (*sql.DB, *pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(ctx, set.ConnectTimeout)
	defer cancel()

	db, pool, err := open(ctx)
	if err != nil {
		return nil, nil, err
	}

	if pool != nil {
		err = pool.Ping(ctx)
	} else {
		err = db.PingContext(ctx)
	}

	if err == nil {
		return db, pool, nil
	}

	closeErr := db.Close()

	if pool != nil {
		pool.Close()
	}

	return nil, nil, errors.Join(err, closeErr)
}
