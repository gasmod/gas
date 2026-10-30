package apikeystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mydb "github.com/gasmod/gas/auth/internal/apikeystore/mysql"
	pgdb "github.com/gasmod/gas/auth/internal/apikeystore/postgres"
	litedb "github.com/gasmod/gas/auth/internal/apikeystore/sqlite"
	"github.com/gasmod/gas/auth/internal/sqlutil"
)

// postgresAdapter runs the Postgres queries. Scopes are a TEXT[] column.
type postgresAdapter struct {
	// q is the sqlc-generated Postgres query set.
	q *pgdb.Queries
}

var _ querier = (*postgresAdapter)(nil)

// withTx returns a Postgres adapter whose queries run in tx.
func (a *postgresAdapter) withTx(tx *sql.Tx) querier {
	return &postgresAdapter{q: a.q.WithTx(tx)}
}

// getKeyByHash reads one key row.
func (a *postgresAdapter) getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error) {
	r, err := a.q.GetKeyByHash(ctx, keyHash)
	if err != nil {
		//nolint:wrapcheck // Store.GetKeyByHash maps sql.ErrNoRows and wraps the rest.
		return nil, err
	}

	return new(pgKeyRow(r)), nil
}

// updateLastUsed sets a key's last use time.
func (a *postgresAdapter) updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	//nolint:wrapcheck // Store.UpdateLastUsed wraps it.
	return a.q.UpdateLastUsed(ctx, &pgdb.UpdateLastUsedParams{
		ID:       id,
		LastUsed: sqlutil.NullTimeFrom(&lastUsed),
	})
}

// insertKey writes one key row.
func (a *postgresAdapter) insertKey(ctx context.Context, p *InsertKeyParams) error {
	//nolint:wrapcheck // Store.InsertKey wraps it.
	return a.q.InsertKey(ctx, &pgdb.InsertKeyParams{
		ID:        p.ID,
		Subject:   p.Subject,
		Name:      p.Name,
		KeyHash:   p.KeyHash,
		KeyPrefix: p.KeyPrefix,
		Scopes:    cleanScopes(p.Scopes),
		Metadata:  json.RawMessage(p.Metadata),
		ExpiresAt: sqlutil.NullTimeFrom(p.ExpiresAt),
		CreatedAt: p.CreatedAt,
	})
}

// softDeleteKeyByID marks one key deleted.
func (a *postgresAdapter) softDeleteKeyByID(
	ctx context.Context,
	id string,
	deletedAt time.Time,
) error {
	//nolint:wrapcheck // Store.SoftDeleteKeyByID wraps it.
	return a.q.SoftDeleteKeyByID(ctx, &pgdb.SoftDeleteKeyByIDParams{
		ID:        id,
		DeletedAt: sqlutil.NullTimeFrom(&deletedAt),
	})
}

// softDeleteKeysBySubject marks a subject's keys deleted.
func (a *postgresAdapter) softDeleteKeysBySubject(
	ctx context.Context,
	subject string,
	deletedAt time.Time,
) error {
	//nolint:wrapcheck // Store.SoftDeleteKeysBySubject wraps it.
	return a.q.SoftDeleteKeysBySubject(ctx, &pgdb.SoftDeleteKeysBySubjectParams{
		Subject:   subject,
		DeletedAt: sqlutil.NullTimeFrom(&deletedAt),
	})
}

// hardDeleteKeyByID removes one key row.
func (a *postgresAdapter) hardDeleteKeyByID(ctx context.Context, id string) error {
	//nolint:wrapcheck // Store.HardDeleteKeyByID wraps it.
	return a.q.HardDeleteKeyByID(ctx, id)
}

// hardDeleteKeysBySubject removes a subject's key rows.
func (a *postgresAdapter) hardDeleteKeysBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // Store.HardDeleteKeysBySubject wraps it.
	return a.q.HardDeleteKeysBySubject(ctx, subject)
}

// listKeysBySubject reads a subject's keys that are not soft-deleted.
func (a *postgresAdapter) listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListKeysBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListKeysBySubject wraps it.
		return nil, err
	}

	return pgKeyRows(rows), nil
}

// listAllKeysBySubject reads all of a subject's keys.
func (a *postgresAdapter) listAllKeysBySubject(
	ctx context.Context,
	subject string,
) ([]keyRow, error) {
	rows, err := a.q.ListAllKeysBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListKeysBySubject wraps it.
		return nil, err
	}

	return pgKeyRows(rows), nil
}

// deleteExpiredKeys removes the rows that expired before cutoff.
func (a *postgresAdapter) deleteExpiredKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredKeys wraps it.
	return a.q.DeleteExpiredKeys(ctx, sqlutil.NullTimeFrom(&cutoff))
}

// pgKeyRows converts Postgres rows to keyRows.
func pgKeyRows(rows []*pgdb.GasAuthApiKey) []keyRow {
	out := make([]keyRow, len(rows))
	for i := range rows {
		out[i] = pgKeyRow(rows[i])
	}

	return out
}

// pgKeyRow converts one Postgres row to a keyRow.
func pgKeyRow(r *pgdb.GasAuthApiKey) keyRow {
	return keyRow{
		ID:        r.ID,
		Name:      r.Name,
		KeyPrefix: r.KeyPrefix,
		Metadata:  r.Metadata,
		Subject:   r.Subject,
		Scopes:    r.Scopes,
		CreatedAt: r.CreatedAt,
		LastUsed:  sqlutil.TimeFromNull(r.LastUsed),
		ExpiresAt: sqlutil.TimeFromNull(r.ExpiresAt),
		DeletedAt: sqlutil.TimeFromNull(r.DeletedAt),
	}
}

// mysqlAdapter runs the MySQL queries. Scopes are comma-joined TEXT. It
// truncates every time it passes to a query to the second first, as
// package sqlutil describes.
type mysqlAdapter struct {
	// q is the sqlc-generated MySQL query set.
	q *mydb.Queries
}

var _ querier = (*mysqlAdapter)(nil)

// withTx returns a MySQL adapter whose queries run in tx.
func (a *mysqlAdapter) withTx(tx *sql.Tx) querier {
	return &mysqlAdapter{q: a.q.WithTx(tx)}
}

// getKeyByHash reads one key row.
func (a *mysqlAdapter) getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error) {
	r, err := a.q.GetKeyByHash(ctx, keyHash)
	if err != nil {
		//nolint:wrapcheck // Store.GetKeyByHash maps sql.ErrNoRows and wraps the rest.
		return nil, err
	}

	return new(mysqlKeyRow(r)), nil
}

// updateLastUsed sets a key's last use time.
func (a *mysqlAdapter) updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	//nolint:wrapcheck // Store.UpdateLastUsed wraps it.
	return a.q.UpdateLastUsed(ctx, &mydb.UpdateLastUsedParams{
		LastUsed: sqlutil.NullTimeFrom(new(sqlutil.TruncateToSecond(lastUsed))),
		ID:       id,
	})
}

// insertKey writes one key row.
func (a *mysqlAdapter) insertKey(ctx context.Context, p *InsertKeyParams) error {
	//nolint:wrapcheck // Store.InsertKey wraps it.
	return a.q.InsertKey(ctx, &mydb.InsertKeyParams{
		ID:        p.ID,
		Subject:   p.Subject,
		Name:      p.Name,
		KeyHash:   p.KeyHash,
		KeyPrefix: p.KeyPrefix,
		Scopes:    joinScopes(p.Scopes),
		Metadata:  json.RawMessage(p.Metadata),
		ExpiresAt: sqlutil.NullTimeFrom(sqlutil.TruncateToSecondPtr(p.ExpiresAt)),
		CreatedAt: sqlutil.TruncateToSecond(p.CreatedAt),
	})
}

// softDeleteKeyByID marks one key deleted.
func (a *mysqlAdapter) softDeleteKeyByID(
	ctx context.Context,
	id string,
	deletedAt time.Time,
) error {
	//nolint:wrapcheck // Store.SoftDeleteKeyByID wraps it.
	return a.q.SoftDeleteKeyByID(ctx, &mydb.SoftDeleteKeyByIDParams{
		ID:        id,
		DeletedAt: sqlutil.NullTimeFrom(new(sqlutil.TruncateToSecond(deletedAt))),
	})
}

// softDeleteKeysBySubject marks a subject's keys deleted.
func (a *mysqlAdapter) softDeleteKeysBySubject(
	ctx context.Context,
	subject string,
	deletedAt time.Time,
) error {
	//nolint:wrapcheck // Store.SoftDeleteKeysBySubject wraps it.
	return a.q.SoftDeleteKeysBySubject(ctx, &mydb.SoftDeleteKeysBySubjectParams{
		Subject:   subject,
		DeletedAt: sqlutil.NullTimeFrom(new(sqlutil.TruncateToSecond(deletedAt))),
	})
}

// hardDeleteKeyByID removes one key row.
func (a *mysqlAdapter) hardDeleteKeyByID(ctx context.Context, id string) error {
	//nolint:wrapcheck // Store.HardDeleteKeyByID wraps it.
	return a.q.HardDeleteKeyByID(ctx, id)
}

// hardDeleteKeysBySubject removes a subject's key rows.
func (a *mysqlAdapter) hardDeleteKeysBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // Store.HardDeleteKeysBySubject wraps it.
	return a.q.HardDeleteKeysBySubject(ctx, subject)
}

// listKeysBySubject reads a subject's keys that are not soft-deleted.
func (a *mysqlAdapter) listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListKeysBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListKeysBySubject wraps it.
		return nil, err
	}

	return mysqlKeyRows(rows), nil
}

// listAllKeysBySubject reads all of a subject's keys.
func (a *mysqlAdapter) listAllKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListAllKeysBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListKeysBySubject wraps it.
		return nil, err
	}

	return mysqlKeyRows(rows), nil
}

// deleteExpiredKeys removes the rows that expired before cutoff.
func (a *mysqlAdapter) deleteExpiredKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredKeys wraps it.
	return a.q.DeleteExpiredKeys(ctx, sqlutil.NullTimeFrom(new(sqlutil.TruncateToSecond(cutoff))))
}

// mysqlKeyRows converts MySQL rows to keyRows.
func mysqlKeyRows(rows []*mydb.GasAuthApiKey) []keyRow {
	out := make([]keyRow, len(rows))
	for i := range rows {
		out[i] = mysqlKeyRow(rows[i])
	}

	return out
}

// mysqlKeyRow converts one MySQL row to a keyRow.
func mysqlKeyRow(r *mydb.GasAuthApiKey) keyRow {
	return keyRow{
		ID:        r.ID,
		Name:      r.Name,
		KeyPrefix: r.KeyPrefix,
		Metadata:  r.Metadata,
		Subject:   r.Subject,
		Scopes:    splitScopes(r.Scopes),
		CreatedAt: r.CreatedAt,
		LastUsed:  sqlutil.TimeFromNull(r.LastUsed),
		ExpiresAt: sqlutil.TimeFromNull(r.ExpiresAt),
		DeletedAt: sqlutil.TimeFromNull(r.DeletedAt),
	}
}

// sqliteAdapter runs the SQLite queries, converting times to and from the
// text SQLite keeps them as. Scopes are comma-joined TEXT.
type sqliteAdapter struct {
	// q is the sqlc-generated SQLite query set.
	q *litedb.Queries
}

var _ querier = (*sqliteAdapter)(nil)

// withTx returns a SQLite adapter whose queries run in tx.
func (a *sqliteAdapter) withTx(tx *sql.Tx) querier {
	return &sqliteAdapter{q: a.q.WithTx(tx)}
}

// getKeyByHash reads one key row. A stored time that does not parse is an
// error.
func (a *sqliteAdapter) getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error) {
	r, err := a.q.GetKeyByHash(ctx, keyHash)
	if err != nil {
		//nolint:wrapcheck // Store.GetKeyByHash maps sql.ErrNoRows and wraps the rest.
		return nil, err
	}

	row, err := sqliteKeyRow(r)
	if err != nil {
		return nil, err
	}

	return &row, nil
}

// updateLastUsed sets a key's last use time.
func (a *sqliteAdapter) updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	//nolint:wrapcheck // Store.UpdateLastUsed wraps it.
	return a.q.UpdateLastUsed(ctx, &litedb.UpdateLastUsedParams{
		LastUsed: sqlutil.FormatSQLiteTimePtr(&lastUsed),
		ID:       id,
	})
}

// insertKey writes one key row.
func (a *sqliteAdapter) insertKey(ctx context.Context, p *InsertKeyParams) error {
	//nolint:wrapcheck // Store.InsertKey wraps it.
	return a.q.InsertKey(ctx, &litedb.InsertKeyParams{
		ID:        p.ID,
		Subject:   p.Subject,
		Name:      p.Name,
		KeyHash:   p.KeyHash,
		KeyPrefix: p.KeyPrefix,
		Scopes:    joinScopes(p.Scopes),
		Metadata:  string(p.Metadata),
		ExpiresAt: sqlutil.FormatSQLiteTimePtr(p.ExpiresAt),
		CreatedAt: sqlutil.FormatSQLiteTime(p.CreatedAt),
	})
}

// softDeleteKeyByID marks one key deleted.
func (a *sqliteAdapter) softDeleteKeyByID(
	ctx context.Context,
	id string,
	deletedAt time.Time,
) error {
	//nolint:wrapcheck // Store.SoftDeleteKeyByID wraps it.
	return a.q.SoftDeleteKeyByID(ctx, &litedb.SoftDeleteKeyByIDParams{
		ID:        id,
		DeletedAt: sqlutil.FormatSQLiteTimePtr(&deletedAt),
	})
}

// softDeleteKeysBySubject marks a subject's keys deleted.
func (a *sqliteAdapter) softDeleteKeysBySubject(
	ctx context.Context,
	subject string,
	deletedAt time.Time,
) error {
	//nolint:wrapcheck // Store.SoftDeleteKeysBySubject wraps it.
	return a.q.SoftDeleteKeysBySubject(ctx, &litedb.SoftDeleteKeysBySubjectParams{
		Subject:   subject,
		DeletedAt: sqlutil.FormatSQLiteTimePtr(&deletedAt),
	})
}

// hardDeleteKeyByID removes one key row.
func (a *sqliteAdapter) hardDeleteKeyByID(ctx context.Context, id string) error {
	//nolint:wrapcheck // Store.HardDeleteKeyByID wraps it.
	return a.q.HardDeleteKeyByID(ctx, id)
}

// hardDeleteKeysBySubject removes a subject's key rows.
func (a *sqliteAdapter) hardDeleteKeysBySubject(ctx context.Context, subject string) error {
	//nolint:wrapcheck // Store.HardDeleteKeysBySubject wraps it.
	return a.q.HardDeleteKeysBySubject(ctx, subject)
}

// listKeysBySubject reads a subject's keys that are not soft-deleted.
func (a *sqliteAdapter) listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListKeysBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListKeysBySubject wraps it.
		return nil, err
	}

	return sqliteKeyRows(rows)
}

// listAllKeysBySubject reads all of a subject's keys.
func (a *sqliteAdapter) listAllKeysBySubject(
	ctx context.Context,
	subject string,
) ([]keyRow, error) {
	rows, err := a.q.ListAllKeysBySubject(ctx, subject)
	if err != nil {
		//nolint:wrapcheck // Store.ListKeysBySubject wraps it.
		return nil, err
	}

	return sqliteKeyRows(rows)
}

// deleteExpiredKeys removes the rows that expired before cutoff. The
// stored text sorts as the time does, since it is UTC in a fixed layout.
func (a *sqliteAdapter) deleteExpiredKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredKeys wraps it.
	return a.q.DeleteExpiredKeys(ctx, sqlutil.FormatSQLiteTimePtr(&cutoff))
}

// sqliteKeyRows converts SQLite rows to keyRows, failing on the first time
// that does not parse.
func sqliteKeyRows(rows []*litedb.GasAuthApiKey) ([]keyRow, error) {
	out := make([]keyRow, len(rows))

	for i := range rows {
		row, err := sqliteKeyRow(rows[i])
		if err != nil {
			return nil, err
		}

		out[i] = row
	}

	return out, nil
}

// sqliteKeyRow converts one SQLite row to a keyRow. A stored time that
// does not parse is an error naming its column.
func sqliteKeyRow(r *litedb.GasAuthApiKey) (keyRow, error) {
	createdAt, err := sqlutil.ParseSQLiteTime(r.CreatedAt)
	if err != nil {
		return keyRow{}, fmt.Errorf("created_at: %w", err)
	}

	lastUsed, err := sqlutil.ParseSQLiteTimePtr(r.LastUsed)
	if err != nil {
		return keyRow{}, fmt.Errorf("last_used: %w", err)
	}

	expiresAt, err := sqlutil.ParseSQLiteTimePtr(r.ExpiresAt)
	if err != nil {
		return keyRow{}, fmt.Errorf("expires_at: %w", err)
	}

	deletedAt, err := sqlutil.ParseSQLiteTimePtr(r.DeletedAt)
	if err != nil {
		return keyRow{}, fmt.Errorf("deleted_at: %w", err)
	}

	return keyRow{
		ID:        r.ID,
		Name:      r.Name,
		KeyPrefix: r.KeyPrefix,
		Scopes:    splitScopes(r.Scopes),
		LastUsed:  lastUsed,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
		DeletedAt: deletedAt,
		Metadata:  []byte(r.Metadata),
		Subject:   r.Subject,
	}, nil
}

// cleanScopes trims each scope and drops the empty ones. It never returns
// nil, so Postgres stores an empty array rather than NULL.
func cleanScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))

	for _, s := range scopes {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}

	return out
}

// splitScopes reads comma-joined scopes, as cleanScopes leaves them.
func splitScopes(s string) []string {
	return cleanScopes(strings.Split(s, ","))
}

// joinScopes writes scopes comma-joined, after cleanScopes.
func joinScopes(scopes []string) string {
	return strings.Join(cleanScopes(scopes), ",")
}
