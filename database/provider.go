package database

import (
	"context"
	"fmt"

	"github.com/gasmod/gas"
)

// Compile-time check: Service implements gas.DatabaseProvider.
var _ gas.DatabaseProvider = (*Service)(nil)

// Query executes a query that returns rows. The returned gas.Rows is
// backed by *sql.Rows which natively satisfies the interface.
func (s *Service) Query(ctx context.Context, query string, args ...any) (gas.Rows, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("%s: service is closed", s.Name())
	}
	if s.db == nil {
		return nil, fmt.Errorf("%s: not initialized", s.Name())
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: query: %w", s.Name(), err)
	}
	return rows, nil
}

// Exec executes a query that doesn't return rows. The returned
// gas.Result is backed by sql.Result which natively satisfies the interface.
func (s *Service) Exec(ctx context.Context, query string, args ...any) (gas.Result, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("%s: service is closed", s.Name())
	}
	if s.db == nil {
		return nil, fmt.Errorf("%s: not initialized", s.Name())
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: exec: %w", s.Name(), err)
	}
	return result, nil
}
