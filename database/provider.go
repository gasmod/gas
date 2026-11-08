package database

import (
	"context"

	"github.com/gasmod/gas"
)

// Query runs a query that returns rows, on DB. The gas.Rows is the
// *sql.Rows, which the caller must close. A statement's error comes back
// wrapped, after `query: `; a connection-level failure wraps ErrConnect,
// as described in the package documentation.
func (s *Service) Query(ctx context.Context, query string, args ...any) (gas.Rows, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, err
	}

	//nolint:rowserrcheck // the caller owns rows and checks Err.
	rows, err := st.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, runtimeError(ctx, "query", err)
	}

	return rows, nil
}

// Exec runs a statement that returns no rows, on DB. The gas.Result is
// the sql.Result. Its errors are as Query's, after `exec: `.
func (s *Service) Exec(ctx context.Context, query string, args ...any) (gas.Result, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, err
	}

	result, err := st.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, runtimeError(ctx, "exec", err)
	}

	return result, nil
}

// opConnection is the operation SafeError names.
const opConnection = "connection"

// SafeError returns err unchanged when it describes a statement, and, in
// place of a connection-level failure, the error the service's own
// methods return for one: `connection: ` and a fixed reason, wrapping
// ErrConnect and not err, as described in the package documentation. A
// nil err is nil. It is for code that runs statements on DB itself, such
// as migrate, and works on a service in any state.
func (*Service) SafeError(err error) error {
	if err == nil || !connectionLevel(err) {
		return err
	}

	return connectError(context.Background(), opConnection, err)
}
