package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// The reasons a connect or ping failure is reported with, as described in
// the package documentation.
const (
	// reasonTimedOut is a deadline that passed.
	reasonTimedOut = "timed out"
	// reasonRefused is a refused TCP connection.
	reasonRefused = "connection refused"
	// reasonAuth is a server that rejected the credentials.
	reasonAuth = "authentication failed"
	// reasonAccessDenied is a server that denied the user the database.
	reasonAccessDenied = "access denied"
	// reasonNoDatabase is a server without the database asked for.
	reasonNoDatabase = "database does not exist"
	// reasonUnreachable is any other network failure.
	reasonUnreachable = "unreachable"
	// reasonFailed is any other failure.
	reasonFailed = "failed"
)

// The server error codes the reasons are told by.
const (
	// pgInvalidPassword is PostgreSQL's invalid_password.
	pgInvalidPassword = "28P01"
	// pgInvalidAuthorization is PostgreSQL's
	// invalid_authorization_specification.
	pgInvalidAuthorization = "28000"
	// pgInvalidCatalog is PostgreSQL's invalid_catalog_name.
	pgInvalidCatalog = "3D000"
	// mysqlDBAccessDenied is MySQL's ER_DBACCESS_DENIED_ERROR, which a
	// user without global privileges gets for a database that does not
	// exist too.
	mysqlDBAccessDenied = 1044
	// mysqlAccessDenied is MySQL's ER_ACCESS_DENIED_ERROR.
	mysqlAccessDenied = 1045
	// mysqlBadDB is MySQL's ER_BAD_DB_ERROR.
	mysqlBadDB = 1049
)

// connError is a connect or ping failure. Its text is fixed, from the
// operation and the reason, and it wraps ErrConnect and the context
// error that ended the operation, never the driver's error, which can
// hold the configuration.
type connError struct {
	// msg is the error text.
	msg string
	// errs are the wrapped errors.
	errs []error
}

// Error returns the fixed text.
func (e *connError) Error() string { return e.msg }

// Unwrap returns ErrConnect and, when the caller's context ended, its
// error.
func (e *connError) Unwrap() []error { return e.errs }

// connectError returns the failure of op, `connect` or `ping`, from the
// driver error err, wrapping ctx's error when ctx has ended.
func connectError(ctx context.Context, op string, err error) error {
	e := &connError{
		msg:  ServiceName + ": " + op + ": " + reason(err),
		errs: []error{ErrConnect},
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		e.msg += ": " + ctxErr.Error()

		e.errs = append(e.errs, ctxErr)
	}

	return e
}

// runtimeError returns the failure of op, a call on the running handle or
// pool such as `query`, from the driver error err. A connection-level
// failure, as connectionLevel tells it, becomes the fixed text
// connectError makes, wrapping ErrConnect and never err, which can hold
// the configuration. Any other error describes the statement and comes
// back wrapped, after op.
func runtimeError(ctx context.Context, op string, err error) error {
	if connectionLevel(err) {
		return connectError(ctx, op, err)
	}

	return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
}

// connectionLevel reports whether err is a failure to reach the database
// rather than of a statement: an error from opening a connection, which
// pgx reports as a *pgconn.ConnectError and the connectors the service
// opens with mark as phaseError, or, unless a context ended the call, a
// network error or a connection the driver found broken.
func connectionLevel(err error) bool {
	var (
		connectErr *pgconn.ConnectError
		phaseErr   *phaseError
		netErr     net.Error
	)

	switch {
	case errors.As(err, &connectErr), errors.As(err, &phaseErr):
		return true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	default:
		return errors.As(err, &netErr) ||
			errors.Is(err, driver.ErrBadConn) ||
			errors.Is(err, mysql.ErrInvalidConn) ||
			errors.Is(err, pgconn.ErrConnClosed)
	}
}

// reason classifies a driver error into one of the fixed reasons, in the
// order the package documentation lists them.
func reason(err error) string {
	r, ok := serverReason(err)
	if ok {
		return r
	}

	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded),
		pgconn.Timeout(err),
		errors.As(err, &netErr) && netErr.Timeout():
		return reasonTimedOut
	case errors.Is(err, syscall.ECONNREFUSED):
		return reasonRefused
	case errors.As(err, &netErr):
		return reasonUnreachable
	default:
		return reasonFailed
	}
}

// serverReason returns the reason for an error the database server
// reported, and whether it is one the reasons tell apart.
func serverReason(err error) (string, bool) {
	var (
		pgErr *pgconn.PgError
		myErr *mysql.MySQLError
	)

	switch {
	case errors.As(err, &pgErr):
		switch pgErr.Code {
		case pgInvalidPassword, pgInvalidAuthorization:
			return reasonAuth, true
		case pgInvalidCatalog:
			return reasonNoDatabase, true
		}
	case errors.As(err, &myErr):
		switch myErr.Number {
		case mysqlAccessDenied:
			return reasonAuth, true
		case mysqlDBAccessDenied:
			return reasonAccessDenied, true
		case mysqlBadDB:
			return reasonNoDatabase, true
		}
	}

	return "", false
}
