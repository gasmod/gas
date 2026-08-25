package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mydb "github.com/gasmod/gas/auth/apikey/db/mysql"
	pgdb "github.com/gasmod/gas/auth/apikey/db/postgres"
	litedb "github.com/gasmod/gas/auth/apikey/db/sqlite"
)

// --- PostgreSQL adapter ---

type postgresAdapter struct {
	q *pgdb.Queries
}

func newPostgresAdapter(q *pgdb.Queries) *postgresAdapter {
	return &postgresAdapter{q: q}
}

func (a *postgresAdapter) withTx(tx *sql.Tx) querier {
	return &postgresAdapter{q: a.q.WithTx(tx)}
}

func (a *postgresAdapter) getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error) {
	r, err := a.q.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, fmt.Errorf("postgresAdapter: %w", err)
	}
	return new(pgRowToKeyRow(r)), nil
}

func (a *postgresAdapter) updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	err := a.q.UpdateLastUsed(ctx, &pgdb.UpdateLastUsedParams{
		ID:       id,
		LastUsed: sql.NullTime{Time: lastUsed, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("postgresAdapter: %w", err)
	}
	return nil
}

func (a *postgresAdapter) insertKey(ctx context.Context, p InsertKeyParams) error {
	params := &pgdb.InsertKeyParams{
		ID:        p.ID,
		Subject:   p.Subject,
		Name:      p.Name,
		KeyHash:   p.KeyHash,
		KeyPrefix: p.KeyPrefix,
		Scopes:    cleanStringSlice(p.Scopes),
		Metadata:  json.RawMessage(p.Metadata),
		CreatedAt: p.CreatedAt,
	}

	if p.ExpiresAt != nil {
		params.ExpiresAt = sql.NullTime{Time: *p.ExpiresAt, Valid: true}
	}

	err := a.q.InsertKey(ctx, params)
	if err != nil {
		return fmt.Errorf("postgresAdapter: %w", err)
	}
	return nil
}

func (a *postgresAdapter) softDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error {
	err := a.q.SoftDeleteKeyByID(ctx, &pgdb.SoftDeleteKeyByIDParams{
		ID:        id,
		DeletedAt: sql.NullTime{Time: deletedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("postgresAdapter: %w", err)
	}
	return nil
}

func (a *postgresAdapter) softDeleteKeysBySubject(ctx context.Context, subject string, deletedAt time.Time) error {
	err := a.q.SoftDeleteKeysBySubject(ctx, &pgdb.SoftDeleteKeysBySubjectParams{
		Subject:   subject,
		DeletedAt: sql.NullTime{Time: deletedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("postgresAdapter: %w", err)
	}
	return nil
}

func (a *postgresAdapter) hardDeleteKeyByID(ctx context.Context, id string) error {
	err := a.q.HardDeleteKeyByID(ctx, id)
	if err != nil {
		return fmt.Errorf("postgresAdapter: %w", err)
	}
	return nil
}

func (a *postgresAdapter) hardDeleteKeysBySubject(ctx context.Context, subject string) error {
	err := a.q.HardDeleteKeysBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("postgresAdapter: %w", err)
	}
	return nil
}

func (a *postgresAdapter) listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListKeysBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("postgresAdapter: %w", err)
	}
	return pgKeyRowsToList(rows), nil
}

func (a *postgresAdapter) listAllKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListAllKeysBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("postgresAdapter: %w", err)
	}
	return pgKeyRowsToList(rows), nil
}

func pgKeyRowsToList(rows []*pgdb.GasAuthApiKey) []keyRow {
	out := make([]keyRow, len(rows))
	for i := range rows {
		out[i] = pgRowToKeyRow(rows[i])
	}
	return out
}

func pgRowToKeyRow(row *pgdb.GasAuthApiKey) keyRow {
	key := keyRow{
		ID:        row.ID,
		Name:      row.Name,
		KeyPrefix: row.KeyPrefix,
		Metadata:  row.Metadata,
		Subject:   row.Subject,
		KeyHash:   row.KeyHash,
		Scopes:    row.Scopes,
		CreatedAt: row.CreatedAt,
	}
	if row.LastUsed.Valid {
		key.LastUsed = &row.LastUsed.Time
	}
	if row.ExpiresAt.Valid {
		key.ExpiresAt = &row.ExpiresAt.Time
	}
	if row.DeletedAt.Valid {
		key.DeletedAt = &row.DeletedAt.Time
	}
	return key
}

// --- MySQL adapter ---

type mysqlAdapter struct {
	q *mydb.Queries
}

func newMySQLAdapter(q *mydb.Queries) *mysqlAdapter {
	return &mysqlAdapter{q: q}
}

func (a *mysqlAdapter) withTx(tx *sql.Tx) querier {
	return &mysqlAdapter{q: a.q.WithTx(tx)}
}

func (a *mysqlAdapter) getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error) {
	r, err := a.q.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, fmt.Errorf("mysqlAdapter: %w", err)
	}
	return new(mysqlRowToKeyRow(r)), nil
}

func (a *mysqlAdapter) updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	err := a.q.UpdateLastUsed(ctx, &mydb.UpdateLastUsedParams{
		LastUsed: sql.NullTime{Time: lastUsed, Valid: true},
		ID:       id,
	})
	if err != nil {
		return fmt.Errorf("mysqlAdapter: %w", err)
	}
	return nil
}

func (a *mysqlAdapter) insertKey(ctx context.Context, p InsertKeyParams) error {
	params := &mydb.InsertKeyParams{
		ID:        p.ID,
		Subject:   p.Subject,
		Name:      p.Name,
		KeyHash:   p.KeyHash,
		KeyPrefix: p.KeyPrefix,
		Scopes:    joinCommas(p.Scopes),
		Metadata:  json.RawMessage(p.Metadata),
		CreatedAt: p.CreatedAt,
	}

	if p.ExpiresAt != nil {
		params.ExpiresAt = sql.NullTime{Time: *p.ExpiresAt, Valid: true}
	}

	err := a.q.InsertKey(ctx, params)
	if err != nil {
		return fmt.Errorf("mysqlAdapter: %w", err)
	}
	return nil
}

func (a *mysqlAdapter) softDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error {
	err := a.q.SoftDeleteKeyByID(ctx, &mydb.SoftDeleteKeyByIDParams{
		ID:        id,
		DeletedAt: sql.NullTime{Time: deletedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mysqlAdapter: %w", err)
	}
	return nil
}

func (a *mysqlAdapter) softDeleteKeysBySubject(ctx context.Context, subject string, deletedAt time.Time) error {
	err := a.q.SoftDeleteKeysBySubject(ctx, &mydb.SoftDeleteKeysBySubjectParams{
		Subject:   subject,
		DeletedAt: sql.NullTime{Time: deletedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mysqlAdapter: %w", err)
	}
	return nil
}

func (a *mysqlAdapter) hardDeleteKeyByID(ctx context.Context, id string) error {
	err := a.q.HardDeleteKeyByID(ctx, id)
	if err != nil {
		return fmt.Errorf("mysqlAdapter: %w", err)
	}
	return nil
}

func (a *mysqlAdapter) hardDeleteKeysBySubject(ctx context.Context, subject string) error {
	err := a.q.HardDeleteKeysBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("mysqlAdapter: %w", err)
	}
	return nil
}

func (a *mysqlAdapter) listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListKeysBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("mysqlAdapter: %w", err)
	}
	return mysqlKeyRowsToList(rows), nil
}

func (a *mysqlAdapter) listAllKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListAllKeysBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("mysqlAdapter: %w", err)
	}
	return mysqlKeyRowsToList(rows), nil
}

func mysqlKeyRowsToList(rows []*mydb.GasAuthApiKey) []keyRow {
	out := make([]keyRow, len(rows))
	for i := range rows {
		out[i] = mysqlRowToKeyRow(rows[i])
	}
	return out
}

func mysqlRowToKeyRow(row *mydb.GasAuthApiKey) keyRow {
	key := keyRow{
		ID:        row.ID,
		Name:      row.Name,
		KeyPrefix: row.KeyPrefix,
		Metadata:  row.Metadata,
		Subject:   row.Subject,
		KeyHash:   row.KeyHash,
		Scopes:    splitCommas(row.Scopes),
		CreatedAt: row.CreatedAt,
	}
	if row.LastUsed.Valid {
		key.LastUsed = &row.LastUsed.Time
	}
	if row.ExpiresAt.Valid {
		key.ExpiresAt = &row.ExpiresAt.Time
	}
	if row.DeletedAt.Valid {
		key.DeletedAt = &row.DeletedAt.Time
	}
	return key
}

// --- SQLite adapter ---

type sqliteAdapter struct {
	q *litedb.Queries
}

func newSQLiteAdapter(q *litedb.Queries) *sqliteAdapter {
	return &sqliteAdapter{q: q}
}

func (a *sqliteAdapter) withTx(tx *sql.Tx) querier {
	return &sqliteAdapter{q: a.q.WithTx(tx)}
}

func (a *sqliteAdapter) getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error) {
	r, err := a.q.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, fmt.Errorf("sqliteAdapter: %w", err)
	}
	key, kErr := sqliteRowToKeyRow(r)
	if kErr != nil {
		return nil, fmt.Errorf("sqliteAdapter: %w", kErr)
	}
	return &key, nil
}

func (a *sqliteAdapter) updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	err := a.q.UpdateLastUsed(ctx, &litedb.UpdateLastUsedParams{
		LastUsed: new(formatSQLiteTime(lastUsed)),
		ID:       id,
	})
	if err != nil {
		return fmt.Errorf("sqliteAdapter: %w", err)
	}
	return nil
}

func (a *sqliteAdapter) insertKey(ctx context.Context, p InsertKeyParams) error {
	var expiresAt *string
	if p.ExpiresAt != nil {
		expiresAt = new(formatSQLiteTime(*p.ExpiresAt))
	}
	err := a.q.InsertKey(ctx, &litedb.InsertKeyParams{
		ID:        p.ID,
		Subject:   p.Subject,
		Name:      p.Name,
		KeyHash:   p.KeyHash,
		KeyPrefix: p.KeyPrefix,
		Scopes:    joinCommas(p.Scopes),
		Metadata:  string(p.Metadata),
		ExpiresAt: expiresAt,
		CreatedAt: formatSQLiteTime(p.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("sqliteAdapter: %w", err)
	}
	return nil
}

func (a *sqliteAdapter) softDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error {
	err := a.q.SoftDeleteKeyByID(ctx, &litedb.SoftDeleteKeyByIDParams{
		ID:        id,
		DeletedAt: new(formatSQLiteTime(deletedAt)),
	})
	if err != nil {
		return fmt.Errorf("sqliteAdapter: %w", err)
	}
	return nil
}

func (a *sqliteAdapter) softDeleteKeysBySubject(ctx context.Context, subject string, deletedAt time.Time) error {
	err := a.q.SoftDeleteKeysBySubject(ctx, &litedb.SoftDeleteKeysBySubjectParams{
		Subject:   subject,
		DeletedAt: new(formatSQLiteTime(deletedAt)),
	})
	if err != nil {
		return fmt.Errorf("sqliteAdapter: %w", err)
	}
	return nil
}

func (a *sqliteAdapter) hardDeleteKeyByID(ctx context.Context, id string) error {
	err := a.q.HardDeleteKeyByID(ctx, id)
	if err != nil {
		return fmt.Errorf("sqliteAdapter: %w", err)
	}
	return nil
}

func (a *sqliteAdapter) hardDeleteKeysBySubject(ctx context.Context, subject string) error {
	err := a.q.HardDeleteKeysBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("sqliteAdapter: %w", err)
	}
	return nil
}

func (a *sqliteAdapter) listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListKeysBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("sqliteAdapter: %w", err)
	}
	return sqliteKeyRowsToList(rows)
}

func (a *sqliteAdapter) listAllKeysBySubject(ctx context.Context, subject string) ([]keyRow, error) {
	rows, err := a.q.ListAllKeysBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("sqliteAdapter: %w", err)
	}
	return sqliteKeyRowsToList(rows)
}

func sqliteKeyRowsToList(rows []*litedb.GasAuthApiKey) ([]keyRow, error) {
	out := make([]keyRow, len(rows))
	var err error
	for i := range rows {
		out[i], err = sqliteRowToKeyRow(rows[i])
		if err != nil {
			return nil, fmt.Errorf("sqliteAdapter: %w", err)
		}
	}
	return out, nil
}

func sqliteRowToKeyRow(row *litedb.GasAuthApiKey) (keyRow, error) {
	var lastUsed, expiresAt, deletedAt *time.Time
	if row.LastUsed != nil {
		t, tErr := parseSQLiteTime(*row.LastUsed)
		if tErr != nil {
			return keyRow{}, fmt.Errorf("sqliteAdapter: %w", tErr)
		}
		lastUsed = &t
	}
	if row.ExpiresAt != nil {
		t, tErr := parseSQLiteTime(*row.ExpiresAt)
		if tErr != nil {
			return keyRow{}, fmt.Errorf("sqliteAdapter: %w", tErr)
		}
		expiresAt = &t
	}
	if row.DeletedAt != nil {
		t, tErr := parseSQLiteTime(*row.DeletedAt)
		if tErr != nil {
			return keyRow{}, fmt.Errorf("sqliteAdapter: %w", tErr)
		}
		deletedAt = &t
	}
	createdAt, cErr := parseSQLiteTime(row.CreatedAt)
	if cErr != nil {
		return keyRow{}, fmt.Errorf("sqliteAdapter: %w", cErr)
	}
	return keyRow{
		ID:        row.ID,
		Name:      row.Name,
		KeyPrefix: row.KeyPrefix,
		Scopes:    splitCommas(row.Scopes),
		LastUsed:  lastUsed,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
		DeletedAt: deletedAt,
		Metadata:  []byte(row.Metadata),
		Subject:   row.Subject,
		KeyHash:   row.KeyHash,
	}, nil
}

// Shared helpers

const sqliteTimeLayout = "2006-01-02 15:04:05"

func parseSQLiteTime(s string) (time.Time, error) {
	t, err := time.Parse(sqliteTimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqliteAdapter: %w", err)
	}
	return t, nil
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}

func cleanStringSlice(s []string) []string {
	out := make([]string, 0, len(s))
	if len(s) == 0 {
		return out
	}
	for i := range s {
		if p := strings.TrimSpace(s[i]); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitCommas(s string) []string {
	return cleanStringSlice(strings.Split(s, ","))
}

func joinCommas(s []string) string {
	return strings.Join(cleanStringSlice(s), ",")
}
