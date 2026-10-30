package tokenstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gasmod/gas/auth/internal/sqlutil"
	mydb "github.com/gasmod/gas/auth/internal/tokenstore/mysql"
	pgdb "github.com/gasmod/gas/auth/internal/tokenstore/postgres"
	litedb "github.com/gasmod/gas/auth/internal/tokenstore/sqlite"
)

// postgresAdapter runs the Postgres queries.
type postgresAdapter struct {
	// q is the sqlc-generated Postgres query set.
	q *pgdb.Queries
}

var _ querier = (*postgresAdapter)(nil)

// insertToken writes one token row.
func (a *postgresAdapter) insertToken(ctx context.Context, t *tokenInsert) error {
	//nolint:wrapcheck // Store.InsertToken wraps it.
	return a.q.InsertToken(ctx, &pgdb.InsertTokenParams{
		ID:        t.ID,
		Subject:   t.Subject,
		TokenHash: t.TokenHash,
		Purpose:   t.Purpose,
		CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt,
	})
}

// deleteTokenByHash deletes one token.
func (a *postgresAdapter) deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	//nolint:wrapcheck // Store.DeleteTokenByHash wraps it.
	return a.q.DeleteTokenByHash(ctx, tokenHash)
}

// deleteTokensBySubjectPurpose deletes a subject's tokens of one purpose.
func (a *postgresAdapter) deleteTokensBySubjectPurpose(
	ctx context.Context,
	subject, purpose string,
) error {
	//nolint:wrapcheck // Store.DeleteTokensBySubjectPurpose wraps it.
	return a.q.DeleteTokensBySubjectPurpose(ctx, &pgdb.DeleteTokensBySubjectPurposeParams{
		Subject: subject,
		Purpose: purpose,
	})
}

// deleteExpiredTokens deletes the tokens that expire at or before a time.
func (a *postgresAdapter) deleteExpiredTokens(
	ctx context.Context,
	before time.Time,
) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredTokens wraps it.
	return a.q.DeleteExpiredTokens(ctx, before)
}

// consumeTokenByHash deletes a token with DELETE ... RETURNING.
func (a *postgresAdapter) consumeTokenByHash(
	ctx context.Context,
	tokenHash string,
) (*tokenRow, error) {
	r, err := a.q.ConsumeTokenByHash(ctx, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoToken
	}

	if err != nil {
		//nolint:wrapcheck // Store.ConsumeTokenByHash wraps it.
		return nil, err
	}

	return &tokenRow{
		ID:        r.ID,
		Subject:   r.Subject,
		Purpose:   r.Purpose,
		ExpiresAt: r.ExpiresAt,
	}, nil
}

// mysqlAdapter runs the MySQL queries. MySQL has no DELETE ... RETURNING,
// so consumeTokenByHash selects and deletes in one transaction and uses
// the delete's row count to detect a concurrent consumer. It truncates
// every time it passes to a query to the second first, as package sqlutil
// describes.
type mysqlAdapter struct {
	// db begins the transaction consumeTokenByHash runs in.
	db *sql.DB
	// q is the sqlc-generated MySQL query set.
	q *mydb.Queries
}

var _ querier = (*mysqlAdapter)(nil)

// insertToken writes one token row.
func (a *mysqlAdapter) insertToken(ctx context.Context, t *tokenInsert) error {
	//nolint:wrapcheck // Store.InsertToken wraps it.
	return a.q.InsertToken(ctx, &mydb.InsertTokenParams{
		ID:        t.ID,
		Subject:   t.Subject,
		TokenHash: t.TokenHash,
		Purpose:   t.Purpose,
		CreatedAt: sqlutil.TruncateToSecond(t.CreatedAt),
		ExpiresAt: sqlutil.TruncateToSecond(t.ExpiresAt),
	})
}

// deleteTokenByHash deletes one token.
func (a *mysqlAdapter) deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	//nolint:wrapcheck // Store.DeleteTokenByHash wraps it.
	return a.q.DeleteTokenByHash(ctx, tokenHash)
}

// deleteTokensBySubjectPurpose deletes a subject's tokens of one purpose.
func (a *mysqlAdapter) deleteTokensBySubjectPurpose(
	ctx context.Context,
	subject, purpose string,
) error {
	//nolint:wrapcheck // Store.DeleteTokensBySubjectPurpose wraps it.
	return a.q.DeleteTokensBySubjectPurpose(ctx, &mydb.DeleteTokensBySubjectPurposeParams{
		Subject: subject,
		Purpose: purpose,
	})
}

// deleteExpiredTokens deletes the tokens that expire at or before a time.
func (a *mysqlAdapter) deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredTokens wraps it.
	return a.q.DeleteExpiredTokens(ctx, sqlutil.TruncateToSecond(before))
}

// consumeTokenByHash selects and deletes a token in one transaction. The
// rollback after a commit, or after an earlier failure that is already
// returned, has nothing to report.
func (a *mysqlAdapter) consumeTokenByHash(
	ctx context.Context,
	tokenHash string,
) (*tokenRow, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	qtx := a.q.WithTx(tx)

	r, err := qtx.GetTokenByHash(ctx, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoToken
	}

	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	n, err := qtx.DeleteTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("delete token: %w", err)
	}

	if n == 0 {
		return nil, errNoToken
	}

	err = tx.Commit()
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &tokenRow{
		ID:        r.ID,
		Subject:   r.Subject,
		Purpose:   r.Purpose,
		ExpiresAt: r.ExpiresAt,
	}, nil
}

// sqliteAdapter runs the SQLite queries, converting times to and from the
// text SQLite keeps them as.
type sqliteAdapter struct {
	// q is the sqlc-generated SQLite query set.
	q *litedb.Queries
}

var _ querier = (*sqliteAdapter)(nil)

// insertToken writes one token row.
func (a *sqliteAdapter) insertToken(ctx context.Context, t *tokenInsert) error {
	//nolint:wrapcheck // Store.InsertToken wraps it.
	return a.q.InsertToken(ctx, &litedb.InsertTokenParams{
		ID:        t.ID,
		Subject:   t.Subject,
		TokenHash: t.TokenHash,
		Purpose:   t.Purpose,
		CreatedAt: sqlutil.FormatSQLiteTime(t.CreatedAt),
		ExpiresAt: sqlutil.FormatSQLiteTime(t.ExpiresAt),
	})
}

// deleteTokenByHash deletes one token.
func (a *sqliteAdapter) deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	//nolint:wrapcheck // Store.DeleteTokenByHash wraps it.
	return a.q.DeleteTokenByHash(ctx, tokenHash)
}

// deleteTokensBySubjectPurpose deletes a subject's tokens of one purpose.
func (a *sqliteAdapter) deleteTokensBySubjectPurpose(
	ctx context.Context,
	subject, purpose string,
) error {
	//nolint:wrapcheck // Store.DeleteTokensBySubjectPurpose wraps it.
	return a.q.DeleteTokensBySubjectPurpose(ctx, &litedb.DeleteTokensBySubjectPurposeParams{
		Subject: subject,
		Purpose: purpose,
	})
}

// deleteExpiredTokens deletes the tokens that expire at or before a time.
func (a *sqliteAdapter) deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // Store.DeleteExpiredTokens wraps it.
	return a.q.DeleteExpiredTokens(ctx, sqlutil.FormatSQLiteTime(before))
}

// consumeTokenByHash deletes a token with DELETE ... RETURNING. A stored
// expiry that does not parse is an error; the token is deleted all the
// same.
func (a *sqliteAdapter) consumeTokenByHash(
	ctx context.Context,
	tokenHash string,
) (*tokenRow, error) {
	r, err := a.q.ConsumeTokenByHash(ctx, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoToken
	}

	if err != nil {
		//nolint:wrapcheck // Store.ConsumeTokenByHash wraps it.
		return nil, err
	}

	expiresAt, err := sqlutil.ParseSQLiteTime(r.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("expires_at: %w", err)
	}

	return &tokenRow{
		ID:        r.ID,
		Subject:   r.Subject,
		Purpose:   r.Purpose,
		ExpiresAt: expiresAt,
	}, nil
}
