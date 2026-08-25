package db

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/gasmod/gas"
	mydb "github.com/gasmod/gas/auth/token/db/mysql"
	pgdb "github.com/gasmod/gas/auth/token/db/postgres"
	litedb "github.com/gasmod/gas/auth/token/db/sqlite"
)

//go:embed postgres/20250323003_create_auth_tokens.up.sql
var migrationUpPostgres string

//go:embed postgres/20250323003_create_auth_tokens.down.sql
var migrationDownPostgres string

//go:embed mysql/20250323003_create_auth_tokens.up.sql
var migrationUpMySQL string

//go:embed mysql/20250323003_create_auth_tokens.down.sql
var migrationDownMySQL string

//go:embed sqlite/20250323003_create_auth_tokens.up.sql
var migrationUpSQLite string

//go:embed sqlite/20250323003_create_auth_tokens.down.sql
var migrationDownSQLite string

const serviceName = "gas/auth/token"

// TokenRecord holds the data returned when looking up a token.
type TokenRecord struct {
	ExpiresAt time.Time
	ID        string
	Subject   string
	Purpose   string
}

// Store is a database-backed token store. It delegates to sqlc-generated
// queries and selects the correct dialect adapter at init time.
type Store struct {
	db     gas.DatabaseProvider
	logger gas.Logger
	migMgr gas.MigrationManager
	q      querier
}

// New returns a DI-injectable constructor for Store.
func New() func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) *Store {
	return func(db gas.DatabaseProvider, logger gas.Logger, migMgr gas.MigrationManager) *Store {
		return &Store{
			db:     db,
			logger: logger,
			migMgr: migMgr,
		}
	}
}

// Init selects the correct sqlc adapter and registers migrations.
func (s *Store) Init(serviceName string) error {
	sqlDB := s.db.DB()
	if sqlDB == nil {
		return fmt.Errorf("%s: database not initialized", serviceName)
	}

	var up, down string

	switch s.db.Driver() {
	case "postgres", "pgx":
		up = migrationUpPostgres
		down = migrationDownPostgres
		s.q = newPostgresAdapter(pgdb.New(sqlDB))
	case "mysql":
		up = migrationUpMySQL
		down = migrationDownMySQL
		s.q = newMySQLAdapter(sqlDB, mydb.New(sqlDB))
	case "sqlite", "sqlite3":
		up = migrationUpSQLite
		down = migrationDownSQLite
		s.q = newSQLiteAdapter(litedb.New(sqlDB))
	default:
		return fmt.Errorf("%s: unsupported driver: %q", serviceName, s.db.Driver())
	}

	s.migMgr.Register(serviceName, gas.Migration{
		Version:     "20250323003",
		Description: "create auth tokens table",
		Up:          up,
		Down:        down,
	})

	return nil
}

// ConsumeTokenByHash atomically fetches and deletes a token by its hash,
// returning the record of the deleted row. Returns (nil, nil) if the token
// does not exist or was consumed concurrently by another caller.
func (s *Store) ConsumeTokenByHash(ctx context.Context, tokenHash string) (*TokenRecord, error) {
	row, err := s.q.consumeTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("%s: consume token: %w", serviceName, err)
	}
	if row == nil {
		return nil, nil
	}
	return &TokenRecord{
		ID:        row.ID,
		Subject:   row.Subject,
		Purpose:   row.Purpose,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// InsertToken stores a new token record.
func (s *Store) InsertToken(ctx context.Context, id, subject, tokenHash, purpose string, createdAt, expiresAt time.Time) error {
	if err := s.q.insertToken(ctx, id, subject, tokenHash, purpose, createdAt, expiresAt); err != nil {
		return fmt.Errorf("%s: insert token: %w", serviceName, err)
	}
	return nil
}

// DeleteTokenByHash deletes a token by its hash and returns rows affected.
func (s *Store) DeleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	n, err := s.q.deleteTokenByHash(ctx, tokenHash)
	if err != nil {
		return 0, fmt.Errorf("%s: delete token by hash: %w", serviceName, err)
	}
	return n, nil
}

// DeleteTokensBySubjectPurpose deletes all tokens for a subject with a given purpose.
func (s *Store) DeleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error {
	if err := s.q.deleteTokensBySubjectPurpose(ctx, subject, purpose); err != nil {
		return fmt.Errorf("%s: delete tokens by subject/purpose: %w", serviceName, err)
	}
	return nil
}

// DeleteExpiredTokens deletes tokens that expired before the given time.
func (s *Store) DeleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.deleteExpiredTokens(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("%s: delete expired tokens: %w", serviceName, err)
	}
	return n, nil
}
