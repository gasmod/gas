package db

import (
	"context"
	"time"

	mydb "github.com/gasmod/gas/auth/session/db/mysql"
	pgdb "github.com/gasmod/gas/auth/session/db/postgres"
	litedb "github.com/gasmod/gas/auth/session/db/sqlite"
)

// --- PostgreSQL adapter ---

type postgresAdapter struct {
	q *pgdb.Queries
}

func newPostgresAdapter(q *pgdb.Queries) *postgresAdapter {
	return &postgresAdapter{q: q}
}

func (a *postgresAdapter) getSession(ctx context.Context, id string) (*sessionRow, error) {
	r, err := a.q.GetSession(ctx, id)
	if err != nil {
		//nolint:wrapcheck // wrapped by caller
		return nil, err
	}
	return &sessionRow{
		ID:         r.ID,
		Subject:    r.Subject,
		Metadata:   string(r.Metadata),
		IPAddress:  r.IpAddress,
		UserAgent:  r.UserAgent,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastActive: r.LastActive,
	}, nil
}

func (a *postgresAdapter) insertSession(ctx context.Context, id, subject, metadata, ipAddress, userAgent string, createdAt, expiresAt, lastActive time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.InsertSession(ctx, &pgdb.InsertSessionParams{
		ID:         id,
		Subject:    subject,
		Metadata:   []byte(metadata),
		IpAddress:  ipAddress,
		UserAgent:  userAgent,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
		LastActive: lastActive,
	})
}

func (a *postgresAdapter) extendSession(ctx context.Context, id string, expiresAt, lastActive time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.ExtendSession(ctx, &pgdb.ExtendSessionParams{
		ID:         id,
		ExpiresAt:  expiresAt,
		LastActive: lastActive,
	})
}

func (a *postgresAdapter) deleteSession(ctx context.Context, id string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteSession(ctx, id)
}

func (a *postgresAdapter) deleteSessionsBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteSessionsBySubject(ctx, subject)
}

func (a *postgresAdapter) deleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteExpiredSessions(ctx, before)
}

// --- MySQL adapter ---

type mysqlAdapter struct {
	q *mydb.Queries
}

func newMySQLAdapter(q *mydb.Queries) *mysqlAdapter {
	return &mysqlAdapter{q: q}
}

func (a *mysqlAdapter) getSession(ctx context.Context, id string) (*sessionRow, error) {
	r, err := a.q.GetSession(ctx, id)
	if err != nil {
		//nolint:wrapcheck // wrapped by caller
		return nil, err
	}
	return &sessionRow{
		ID:         r.ID,
		Subject:    r.Subject,
		Metadata:   string(r.Metadata),
		IPAddress:  r.IpAddress,
		UserAgent:  r.UserAgent,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastActive: r.LastActive,
	}, nil
}

func (a *mysqlAdapter) insertSession(ctx context.Context, id, subject, metadata, ipAddress, userAgent string, createdAt, expiresAt, lastActive time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.InsertSession(ctx, &mydb.InsertSessionParams{
		ID:         id,
		Subject:    subject,
		Metadata:   []byte(metadata),
		IpAddress:  ipAddress,
		UserAgent:  userAgent,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
		LastActive: lastActive,
	})
}

func (a *mysqlAdapter) extendSession(ctx context.Context, id string, expiresAt, lastActive time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.ExtendSession(ctx, &mydb.ExtendSessionParams{
		ExpiresAt:  expiresAt,
		LastActive: lastActive,
		ID:         id,
	})
}

func (a *mysqlAdapter) deleteSession(ctx context.Context, id string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteSession(ctx, id)
}

func (a *mysqlAdapter) deleteSessionsBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteSessionsBySubject(ctx, subject)
}

func (a *mysqlAdapter) deleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteExpiredSessions(ctx, before)
}

// --- SQLite adapter ---

type sqliteAdapter struct {
	q *litedb.Queries
}

func newSQLiteAdapter(q *litedb.Queries) *sqliteAdapter {
	return &sqliteAdapter{q: q}
}

func (a *sqliteAdapter) getSession(ctx context.Context, id string) (*sessionRow, error) {
	r, err := a.q.GetSession(ctx, id)
	if err != nil {
		//nolint:wrapcheck // wrapped by caller
		return nil, err
	}
	return &sessionRow{
		ID:         r.ID,
		Subject:    r.Subject,
		Metadata:   r.Metadata,
		IPAddress:  r.IpAddress,
		UserAgent:  r.UserAgent,
		CreatedAt:  parseSQLiteTime(r.CreatedAt),
		ExpiresAt:  parseSQLiteTime(r.ExpiresAt),
		LastActive: parseSQLiteTime(r.LastActive),
	}, nil
}

func (a *sqliteAdapter) insertSession(ctx context.Context, id, subject, metadata, ipAddress, userAgent string, createdAt, expiresAt, lastActive time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.InsertSession(ctx, &litedb.InsertSessionParams{
		ID:         id,
		Subject:    subject,
		Metadata:   metadata,
		IpAddress:  ipAddress,
		UserAgent:  userAgent,
		CreatedAt:  formatSQLiteTime(createdAt),
		ExpiresAt:  formatSQLiteTime(expiresAt),
		LastActive: formatSQLiteTime(lastActive),
	})
}

func (a *sqliteAdapter) extendSession(ctx context.Context, id string, expiresAt, lastActive time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.ExtendSession(ctx, &litedb.ExtendSessionParams{
		ExpiresAt:  formatSQLiteTime(expiresAt),
		LastActive: formatSQLiteTime(lastActive),
		ID:         id,
	})
}

func (a *sqliteAdapter) deleteSession(ctx context.Context, id string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteSession(ctx, id)
}

func (a *sqliteAdapter) deleteSessionsBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteSessionsBySubject(ctx, subject)
}

func (a *sqliteAdapter) deleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteExpiredSessions(ctx, formatSQLiteTime(before))
}

const sqliteTimeLayout = "2006-01-02 15:04:05"

func parseSQLiteTime(s string) time.Time {
	t, _ := time.Parse(sqliteTimeLayout, s)
	return t
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}
