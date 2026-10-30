package tokenstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/sqlutil"
	mydb "github.com/gasmod/gas/auth/internal/tokenstore/mysql"
	pgdb "github.com/gasmod/gas/auth/internal/tokenstore/postgres"
	litedb "github.com/gasmod/gas/auth/internal/tokenstore/sqlite"
)

// migrationsPostgres holds the Postgres migrations.
//
//go:embed postgres/*.up.sql postgres/*.down.sql
var migrationsPostgres embed.FS

// migrationsMySQL holds the MySQL migrations.
//
//go:embed mysql/*.up.sql mysql/*.down.sql
var migrationsMySQL embed.FS

// migrationsSQLite holds the SQLite migrations.
//
//go:embed sqlite/*.up.sql sqlite/*.down.sql
var migrationsSQLite embed.FS

var (
	// ErrNotFound indicates that no token has the hash asked for, or that
	// another caller consumed it first.
	ErrNotFound = errors.New("token not found")
	// ErrNoDatabase indicates that the database provider has no open
	// database when Init runs.
	ErrNoDatabase = errors.New("database not initialized")
)

// storeName prefixes the store's errors.
const storeName = "gas/auth/token-store"

// TokenRecord is what consuming a token returns about it.
type TokenRecord struct {
	// ExpiresAt is when the record stops being valid.
	ExpiresAt time.Time
	// ID identifies the record.
	ID string
	// Subject is the user or client the record belongs to.
	Subject string
	// Purpose is what the token may be used for, such as a password reset.
	Purpose string
}

// Store keeps single-use tokens in a SQL database, as described in the
// package documentation. It is safe for concurrent use once Init has
// returned.
type Store struct {
	// db is the database the store reads and writes.
	db gas.DatabaseProvider
	// logger is kept for the constructor's shape; the store logs nothing.
	logger gas.Logger
	// migMgr receives the store's migrations in Init.
	migMgr gas.MigrationManager
	// q runs the queries of the dialect Init picked.
	q querier
}

// New returns a constructor for a Store, in the shape the dependency
// injection container calls. The constructor never fails.
func New() func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) (*Store, error) {
	return func(db gas.DatabaseProvider, logger gas.Logger, migMgr gas.MigrationManager) (*Store, error) {
		return &Store{
			db:     db,
			logger: logger,
			migMgr: migMgr,
		}, nil
	}
}

// Init picks the dialect from the database's driver name and registers
// the dialect's migrations, owned by service. A nil database, an unknown
// driver, or a failure to register is an error that starts with the
// service's name and wraps ErrNoDatabase, sqlutil.ErrUnsupportedDriver,
// or the migration manager's error.
func (s *Store) Init(service gas.Service) error {
	sqlDB := s.db.DB()
	if sqlDB == nil {
		return fmt.Errorf("%s: %w", service.Name(), ErrNoDatabase)
	}

	dialect, err := sqlutil.DialectOf(s.db.Driver())
	if err != nil {
		return fmt.Errorf("%s: %w", service.Name(), err)
	}

	var migrations embed.FS

	switch dialect {
	case sqlutil.Postgres:
		migrations = migrationsPostgres
		s.q = &postgresAdapter{q: pgdb.New(sqlDB)}
	case sqlutil.MySQL:
		migrations = migrationsMySQL
		s.q = &mysqlAdapter{db: sqlDB, q: mydb.New(sqlDB)}
	case sqlutil.SQLite:
		migrations = migrationsSQLite
		s.q = &sqliteAdapter{q: litedb.New(sqlDB)}
	}

	err = s.migMgr.RegisterFS(service, migrations)
	if err != nil {
		return fmt.Errorf("%s: register migrations: %w", service.Name(), err)
	}

	return nil
}

// ConsumeTokenByHash deletes the token with the given hash and returns
// what it held, in one atomic step. It returns ErrNotFound when there is
// no such token, including when another caller consumed it first, so a
// token is consumed at most once.
func (s *Store) ConsumeTokenByHash(ctx context.Context, tokenHash string) (*TokenRecord, error) {
	row, err := s.q.consumeTokenByHash(ctx, tokenHash)
	if errors.Is(err, errNoToken) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("%s: consume token: %w", storeName, err)
	}

	return &TokenRecord{
		ID:        row.ID,
		Subject:   row.Subject,
		Purpose:   row.Purpose,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// InsertToken stores a new token.
func (s *Store) InsertToken(
	ctx context.Context,
	id, subject, tokenHash, purpose string,
	createdAt, expiresAt time.Time,
) error {
	err := s.q.insertToken(ctx, &tokenInsert{
		ID:        id,
		Subject:   subject,
		TokenHash: tokenHash,
		Purpose:   purpose,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return fmt.Errorf("%s: insert token: %w", storeName, err)
	}

	return nil
}

// DeleteTokenByHash deletes the token with the given hash and returns how
// many rows it deleted, 0 or 1.
func (s *Store) DeleteTokenByHash(ctx context.Context, tokenHash string) (int64, error) {
	n, err := s.q.deleteTokenByHash(ctx, tokenHash)
	if err != nil {
		return 0, fmt.Errorf("%s: delete token by hash: %w", storeName, err)
	}

	return n, nil
}

// DeleteTokensBySubjectPurpose deletes every token of a subject with the
// given purpose.
func (s *Store) DeleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error {
	err := s.q.deleteTokensBySubjectPurpose(ctx, subject, purpose)
	if err != nil {
		return fmt.Errorf("%s: delete tokens by subject and purpose: %w", storeName, err)
	}

	return nil
}

// DeleteExpiredTokens deletes the tokens whose expiry is at or before the
// given time, the tokens that are expired at that time, and returns how
// many it deleted.
func (s *Store) DeleteExpiredTokens(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.deleteExpiredTokens(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("%s: delete expired tokens: %w", storeName, err)
	}

	return n, nil
}
