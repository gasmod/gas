package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mydb "github.com/gasmod/gas/auth/token/db/mysql"
	pgdb "github.com/gasmod/gas/auth/token/db/postgres"
	litedb "github.com/gasmod/gas/auth/token/db/sqlite"
)

// --- PostgreSQL adapter ---

type postgresAdapter struct {
	q *pgdb.Queries
}

func newPostgresAdapter(q *pgdb.Queries) *postgresAdapter {
	return &postgresAdapter{q: q}
}

func (a *postgresAdapter) insertToken(ctx context.Context, id, subject, tokenHash, purpose string, createdAt, expiresAt time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.InsertToken(ctx, &pgdb.InsertTokenParams{
		ID:        id,
		Subject:   subject,
		TokenHash: tokenHash,
		Purpose:   purpose,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
}

func (a *postgresAdapter) deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteTokenByHash(ctx, tokenHash)
}

func (a *postgresAdapter) deleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteTokensBySubjectPurpose(ctx, &pgdb.DeleteTokensBySubjectPurposeParams{
		Subject: subject,
		Purpose: purpose,
	})
}

func (a *postgresAdapter) deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteExpiredTokens(ctx, before)
}

func (a *postgresAdapter) consumeTokenByHash(ctx context.Context, tokenHash string) (*tokenRow, error) {
	r, err := a.q.ConsumeTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		//nolint:wrapcheck // wrapped by caller
		return nil, err
	}
	return &tokenRow{
		ID:        r.ID,
		Subject:   r.Subject,
		Purpose:   r.Purpose,
		ExpiresAt: r.ExpiresAt,
	}, nil
}

// --- MySQL adapter ---

// MySQL (stock, not MariaDB) does not support DELETE ... RETURNING, so
// consumeTokenByHash runs SELECT + DELETE inside a transaction and uses the
// DELETE's rows-affected count to detect concurrent consumption.

type mysqlAdapter struct {
	db *sql.DB
	q  *mydb.Queries
}

func newMySQLAdapter(db *sql.DB, q *mydb.Queries) *mysqlAdapter {
	return &mysqlAdapter{db: db, q: q}
}

func (a *mysqlAdapter) insertToken(ctx context.Context, id, subject, tokenHash, purpose string, createdAt, expiresAt time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.InsertToken(ctx, &mydb.InsertTokenParams{
		ID:        id,
		Subject:   subject,
		TokenHash: tokenHash,
		Purpose:   purpose,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
}

func (a *mysqlAdapter) deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteTokenByHash(ctx, tokenHash)
}

func (a *mysqlAdapter) deleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteTokensBySubjectPurpose(ctx, &mydb.DeleteTokensBySubjectPurposeParams{
		Subject: subject,
		Purpose: purpose,
	})
}

func (a *mysqlAdapter) deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteExpiredTokens(ctx, before)
}

func (a *mysqlAdapter) consumeTokenByHash(ctx context.Context, tokenHash string) (*tokenRow, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := a.q.WithTx(tx)

	r, err := qtx.GetTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get token: %w", err)
	}

	n, err := qtx.DeleteTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("delete token: %w", err)
	}
	if n == 0 {
		return nil, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &tokenRow{
		ID:        r.ID,
		Subject:   r.Subject,
		Purpose:   r.Purpose,
		ExpiresAt: r.ExpiresAt,
	}, nil
}

// --- SQLite adapter ---

type sqliteAdapter struct {
	q *litedb.Queries
}

func newSQLiteAdapter(q *litedb.Queries) *sqliteAdapter {
	return &sqliteAdapter{q: q}
}

func (a *sqliteAdapter) insertToken(ctx context.Context, id, subject, tokenHash, purpose string, createdAt, expiresAt time.Time) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.InsertToken(ctx, &litedb.InsertTokenParams{
		ID:        id,
		Subject:   subject,
		TokenHash: tokenHash,
		Purpose:   purpose,
		CreatedAt: formatSQLiteTime(createdAt),
		ExpiresAt: formatSQLiteTime(expiresAt),
	})
}

func (a *sqliteAdapter) deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteTokenByHash(ctx, tokenHash)
}

func (a *sqliteAdapter) deleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteTokensBySubjectPurpose(ctx, &litedb.DeleteTokensBySubjectPurposeParams{
		Subject: subject,
		Purpose: purpose,
	})
}

func (a *sqliteAdapter) deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // wrapped by caller
	return a.q.DeleteExpiredTokens(ctx, formatSQLiteTime(before))
}

func (a *sqliteAdapter) consumeTokenByHash(ctx context.Context, tokenHash string) (*tokenRow, error) {
	r, err := a.q.ConsumeTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		//nolint:wrapcheck // wrapped by caller
		return nil, err
	}
	exp, expErr := parseSQLiteTime(r.ExpiresAt)
	if expErr != nil {
		return nil, fmt.Errorf("failed to parse token expiration time: %w", expErr)
	}
	return &tokenRow{
		ID:        r.ID,
		Subject:   r.Subject,
		Purpose:   r.Purpose,
		ExpiresAt: exp,
	}, nil
}

const sqliteTimeLayout = "2006-01-02 15:04:05"

func parseSQLiteTime(s string) (time.Time, error) {
	t, err := time.Parse(sqliteTimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse time: %w", err)
	}
	return t, nil
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}
