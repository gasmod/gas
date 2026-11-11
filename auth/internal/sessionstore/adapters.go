package sessionstore

import (
	"context"
	"fmt"
	"time"

	mydb "github.com/gasmod/gas/auth/internal/sessionstore/mysql"
	pgdb "github.com/gasmod/gas/auth/internal/sessionstore/postgres"
	litedb "github.com/gasmod/gas/auth/internal/sessionstore/sqlite"
	"github.com/gasmod/gas/auth/internal/sqlutil"
)

// postgresAdapter runs the Postgres queries.
type postgresAdapter struct {
	// q is the sqlc-generated Postgres query set.
	q *pgdb.Queries
}

var _ querier = (*postgresAdapter)(nil)

// getSession reads one session row.
func (a *postgresAdapter) getSession(ctx context.Context, idHash string) (*sessionRow, error) {
	r, err := a.q.GetSession(ctx, idHash)
	if err != nil {
		//nolint:wrapcheck // Store.GetSession maps sql.ErrNoRows and wraps the rest.
		return nil, err
	}

	return pgRow(r), nil
}

// listSessionsBySubject reads a subject's session rows, newest first.
func (a *postgresAdapter) listSessionsBySubject(
	ctx context.Context,
	subject string,
) ([]*sessionRow, error) {
	rs, err := a.q.ListSessionsBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListSessionsBySubject wraps it.
		return nil, err
	}

	out := make([]*sessionRow, len(rs))
	for i, r := range rs {
		out[i] = pgRow(r)
	}

	return out, nil
}

// insertSession writes one session row.
func (a *postgresAdapter) insertSession(ctx context.Context, row *sessionRow) error {
	//nolint:wrapcheck // Store.InsertSession wraps it.
	return a.q.InsertSession(ctx, &pgdb.InsertSessionParams{
		IDHash:     row.IDHash,
		Subject:    row.Subject,
		Metadata:   []byte(row.Metadata),
		IpAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		CreatedAt:  row.CreatedAt,
		ExpiresAt:  row.ExpiresAt,
		LastActive: row.LastActive,
	})
}

// extendSession sets a session's expiry and last active time.
func (a *postgresAdapter) extendSession(
	ctx context.Context,
	idHash string,
	expiresAt, lastActive time.Time,
) error {
	//nolint:wrapcheck // Store.ExtendSession wraps it.
	return a.q.ExtendSession(ctx, &pgdb.ExtendSessionParams{
		IDHash:     idHash,
		ExpiresAt:  expiresAt,
		LastActive: lastActive,
	})
}

// rotateSession moves a session to a new ID hash and sets what a
// rotation changes.
func (a *postgresAdapter) rotateSession(
	ctx context.Context,
	oldIDHash string,
	row *sessionRow,
) (int64, error) {
	//nolint:wrapcheck // Store.RotateSession wraps it.
	return a.q.RotateSession(ctx, &pgdb.RotateSessionParams{
		NewIDHash:  row.IDHash,
		IpAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		ExpiresAt:  row.ExpiresAt,
		LastActive: row.LastActive,
		OldIDHash:  oldIDHash,
	})
}

// deleteSession deletes one session.
func (a *postgresAdapter) deleteSession(ctx context.Context, idHash string) error {
	_, err := a.q.DeleteSession(ctx, idHash)

	//nolint:wrapcheck // Store.DeleteSession wraps it.
	return err
}

// deleteSessionOfSubject deletes one session if it is subject's.
func (a *postgresAdapter) deleteSessionOfSubject(
	ctx context.Context,
	idHash, subject string,
) error {
	_, err := a.q.DeleteSessionOfSubject(ctx, &pgdb.DeleteSessionOfSubjectParams{
		IDHash:  idHash,
		Subject: subject,
	})

	//nolint:wrapcheck // Store.DeleteSessionOfSubject wraps it.
	return err
}

// deleteOtherSessionsOfSubject deletes a subject's sessions but one.
func (a *postgresAdapter) deleteOtherSessionsOfSubject(
	ctx context.Context,
	subject, keepIDHash string,
) error {
	//nolint:wrapcheck // Store.DeleteOtherSessionsOfSubject wraps it.
	return a.q.DeleteOtherSessionsOfSubject(ctx, &pgdb.DeleteOtherSessionsOfSubjectParams{
		Subject: subject,
		IDHash:  keepIDHash,
	})
}

// deleteSessionsBySubject deletes a subject's sessions.
func (a *postgresAdapter) deleteSessionsBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // Store.DeleteSessionsBySubject wraps it.
	return a.q.DeleteSessionsBySubject(ctx, subject)
}

// deleteExpiredSessions deletes the sessions whose expiry, or creation
// time, is at or before the given one.
func (a *postgresAdapter) deleteExpiredSessions(
	ctx context.Context,
	expiryCutoff, creationCutoff time.Time,
) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredSessions wraps it.
	return a.q.DeleteExpiredSessions(ctx, &pgdb.DeleteExpiredSessionsParams{
		ExpiresAt: expiryCutoff,
		CreatedAt: creationCutoff,
	})
}

// pgRow returns a Postgres session as a row.
func pgRow(r *pgdb.GasAuthSession) *sessionRow {
	return &sessionRow{
		IDHash:     r.IDHash,
		Subject:    r.Subject,
		Metadata:   string(r.Metadata),
		IPAddress:  r.IpAddress,
		UserAgent:  r.UserAgent,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastActive: r.LastActive,
	}
}

// mysqlAdapter runs the MySQL queries. It truncates every time it
// passes to a query to the second first, as package sqlutil describes.
type mysqlAdapter struct {
	// q is the sqlc-generated MySQL query set.
	q *mydb.Queries
}

var _ querier = (*mysqlAdapter)(nil)

// getSession reads one session row.
func (a *mysqlAdapter) getSession(ctx context.Context, idHash string) (*sessionRow, error) {
	r, err := a.q.GetSession(ctx, idHash)
	if err != nil {
		//nolint:wrapcheck // Store.GetSession maps sql.ErrNoRows and wraps the rest.
		return nil, err
	}

	return myRow(r), nil
}

// listSessionsBySubject reads a subject's session rows, newest first.
func (a *mysqlAdapter) listSessionsBySubject(
	ctx context.Context,
	subject string,
) ([]*sessionRow, error) {
	rs, err := a.q.ListSessionsBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListSessionsBySubject wraps it.
		return nil, err
	}

	out := make([]*sessionRow, len(rs))
	for i, r := range rs {
		out[i] = myRow(r)
	}

	return out, nil
}

// insertSession writes one session row.
func (a *mysqlAdapter) insertSession(ctx context.Context, row *sessionRow) error {
	//nolint:wrapcheck // Store.InsertSession wraps it.
	return a.q.InsertSession(ctx, &mydb.InsertSessionParams{
		IDHash:     row.IDHash,
		Subject:    row.Subject,
		Metadata:   []byte(row.Metadata),
		IpAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		CreatedAt:  sqlutil.TruncateToSecond(row.CreatedAt),
		ExpiresAt:  sqlutil.TruncateToSecond(row.ExpiresAt),
		LastActive: sqlutil.TruncateToSecond(row.LastActive),
	})
}

// extendSession sets a session's expiry and last active time.
func (a *mysqlAdapter) extendSession(
	ctx context.Context,
	idHash string,
	expiresAt, lastActive time.Time,
) error {
	//nolint:wrapcheck // Store.ExtendSession wraps it.
	return a.q.ExtendSession(ctx, &mydb.ExtendSessionParams{
		ExpiresAt:  sqlutil.TruncateToSecond(expiresAt),
		LastActive: sqlutil.TruncateToSecond(lastActive),
		IDHash:     idHash,
	})
}

// rotateSession moves a session to a new ID hash and sets what a
// rotation changes.
func (a *mysqlAdapter) rotateSession(
	ctx context.Context,
	oldIDHash string,
	row *sessionRow,
) (int64, error) {
	//nolint:wrapcheck // Store.RotateSession wraps it.
	return a.q.RotateSession(ctx, &mydb.RotateSessionParams{
		NewIDHash:  row.IDHash,
		IpAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		ExpiresAt:  sqlutil.TruncateToSecond(row.ExpiresAt),
		LastActive: sqlutil.TruncateToSecond(row.LastActive),
		OldIDHash:  oldIDHash,
	})
}

// deleteSession deletes one session.
func (a *mysqlAdapter) deleteSession(ctx context.Context, idHash string) error {
	_, err := a.q.DeleteSession(ctx, idHash)

	//nolint:wrapcheck // Store.DeleteSession wraps it.
	return err
}

// deleteSessionOfSubject deletes one session if it is subject's.
func (a *mysqlAdapter) deleteSessionOfSubject(ctx context.Context, idHash, subject string) error {
	_, err := a.q.DeleteSessionOfSubject(ctx, &mydb.DeleteSessionOfSubjectParams{
		IDHash:  idHash,
		Subject: subject,
	})

	//nolint:wrapcheck // Store.DeleteSessionOfSubject wraps it.
	return err
}

// deleteOtherSessionsOfSubject deletes a subject's sessions but one.
func (a *mysqlAdapter) deleteOtherSessionsOfSubject(
	ctx context.Context,
	subject, keepIDHash string,
) error {
	//nolint:wrapcheck // Store.DeleteOtherSessionsOfSubject wraps it.
	return a.q.DeleteOtherSessionsOfSubject(ctx, &mydb.DeleteOtherSessionsOfSubjectParams{
		Subject: subject,
		IDHash:  keepIDHash,
	})
}

// deleteSessionsBySubject deletes a subject's sessions.
func (a *mysqlAdapter) deleteSessionsBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // Store.DeleteSessionsBySubject wraps it.
	return a.q.DeleteSessionsBySubject(ctx, subject)
}

// deleteExpiredSessions deletes the sessions whose expiry, or creation
// time, is at or before the given one.
func (a *mysqlAdapter) deleteExpiredSessions(
	ctx context.Context,
	expiryCutoff, creationCutoff time.Time,
) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredSessions wraps it.
	return a.q.DeleteExpiredSessions(ctx, &mydb.DeleteExpiredSessionsParams{
		ExpiresAt: sqlutil.TruncateToSecond(expiryCutoff),
		CreatedAt: sqlutil.TruncateToSecond(creationCutoff),
	})
}

// myRow returns a MySQL session as a row.
func myRow(r *mydb.GasAuthSession) *sessionRow {
	return &sessionRow{
		IDHash:     r.IDHash,
		Subject:    r.Subject,
		Metadata:   string(r.Metadata),
		IPAddress:  r.IpAddress,
		UserAgent:  r.UserAgent,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastActive: r.LastActive,
	}
}

// sqliteAdapter runs the SQLite queries, converting times to and from the
// text SQLite keeps them as.
type sqliteAdapter struct {
	// q is the sqlc-generated SQLite query set.
	q *litedb.Queries
}

var _ querier = (*sqliteAdapter)(nil)

// getSession reads one session row. A stored time that does not parse is
// an error.
func (a *sqliteAdapter) getSession(ctx context.Context, idHash string) (*sessionRow, error) {
	r, err := a.q.GetSession(ctx, idHash)
	if err != nil {
		//nolint:wrapcheck // Store.GetSession maps sql.ErrNoRows and wraps the rest.
		return nil, err
	}

	return liteRow(r)
}

// listSessionsBySubject reads a subject's session rows, newest first. A
// stored time that does not parse is an error.
func (a *sqliteAdapter) listSessionsBySubject(
	ctx context.Context,
	subject string,
) ([]*sessionRow, error) {
	rs, err := a.q.ListSessionsBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListSessionsBySubject wraps it.
		return nil, err
	}

	out := make([]*sessionRow, len(rs))
	for i, r := range rs {
		out[i], err = liteRow(r)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// liteRow returns a SQLite session as a row, parsing its times. A time
// that does not parse is an error.
func liteRow(r *litedb.GasAuthSession) (*sessionRow, error) {
	createdAt, err := sqlutil.ParseSQLiteTime(r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("created_at: %w", err)
	}

	expiresAt, err := sqlutil.ParseSQLiteTime(r.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("expires_at: %w", err)
	}

	lastActive, err := sqlutil.ParseSQLiteTime(r.LastActive)
	if err != nil {
		return nil, fmt.Errorf("last_active: %w", err)
	}

	return &sessionRow{
		IDHash:     r.IDHash,
		Subject:    r.Subject,
		Metadata:   r.Metadata,
		IPAddress:  r.IpAddress,
		UserAgent:  r.UserAgent,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
		LastActive: lastActive,
	}, nil
}

// insertSession writes one session row.
func (a *sqliteAdapter) insertSession(ctx context.Context, row *sessionRow) error {
	//nolint:wrapcheck // Store.InsertSession wraps it.
	return a.q.InsertSession(ctx, &litedb.InsertSessionParams{
		IDHash:     row.IDHash,
		Subject:    row.Subject,
		Metadata:   row.Metadata,
		IpAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		CreatedAt:  sqlutil.FormatSQLiteTime(row.CreatedAt),
		ExpiresAt:  sqlutil.FormatSQLiteTime(row.ExpiresAt),
		LastActive: sqlutil.FormatSQLiteTime(row.LastActive),
	})
}

// extendSession sets a session's expiry and last active time.
func (a *sqliteAdapter) extendSession(
	ctx context.Context,
	idHash string,
	expiresAt, lastActive time.Time,
) error {
	//nolint:wrapcheck // Store.ExtendSession wraps it.
	return a.q.ExtendSession(ctx, &litedb.ExtendSessionParams{
		ExpiresAt:  sqlutil.FormatSQLiteTime(expiresAt),
		LastActive: sqlutil.FormatSQLiteTime(lastActive),
		IDHash:     idHash,
	})
}

// rotateSession moves a session to a new ID hash and sets what a
// rotation changes.
func (a *sqliteAdapter) rotateSession(
	ctx context.Context,
	oldIDHash string,
	row *sessionRow,
) (int64, error) {
	//nolint:wrapcheck // Store.RotateSession wraps it.
	return a.q.RotateSession(ctx, &litedb.RotateSessionParams{
		NewIDHash:  row.IDHash,
		IpAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		ExpiresAt:  sqlutil.FormatSQLiteTime(row.ExpiresAt),
		LastActive: sqlutil.FormatSQLiteTime(row.LastActive),
		OldIDHash:  oldIDHash,
	})
}

// deleteSession deletes one session.
func (a *sqliteAdapter) deleteSession(ctx context.Context, idHash string) error {
	_, err := a.q.DeleteSession(ctx, idHash)

	//nolint:wrapcheck // Store.DeleteSession wraps it.
	return err
}

// deleteSessionOfSubject deletes one session if it is subject's.
func (a *sqliteAdapter) deleteSessionOfSubject(ctx context.Context, idHash, subject string) error {
	_, err := a.q.DeleteSessionOfSubject(ctx, &litedb.DeleteSessionOfSubjectParams{
		IDHash:  idHash,
		Subject: subject,
	})

	//nolint:wrapcheck // Store.DeleteSessionOfSubject wraps it.
	return err
}

// deleteOtherSessionsOfSubject deletes a subject's sessions but one.
func (a *sqliteAdapter) deleteOtherSessionsOfSubject(
	ctx context.Context,
	subject, keepIDHash string,
) error {
	//nolint:wrapcheck // Store.DeleteOtherSessionsOfSubject wraps it.
	return a.q.DeleteOtherSessionsOfSubject(ctx, &litedb.DeleteOtherSessionsOfSubjectParams{
		Subject: subject,
		IDHash:  keepIDHash,
	})
}

// deleteSessionsBySubject deletes a subject's sessions.
func (a *sqliteAdapter) deleteSessionsBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // Store.DeleteSessionsBySubject wraps it.
	return a.q.DeleteSessionsBySubject(ctx, subject)
}

// deleteExpiredSessions deletes the sessions whose expiry, or creation
// time, is at or before the given one.
func (a *sqliteAdapter) deleteExpiredSessions(
	ctx context.Context,
	expiryCutoff, creationCutoff time.Time,
) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredSessions wraps it.
	return a.q.DeleteExpiredSessions(ctx, &litedb.DeleteExpiredSessionsParams{
		ExpiresAt: sqlutil.FormatSQLiteTime(expiryCutoff),
		CreatedAt: sqlutil.FormatSQLiteTime(creationCutoff),
	})
}
