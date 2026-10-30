package apikeystore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gasmod/gas"
	mydb "github.com/gasmod/gas/auth/internal/apikeystore/mysql"
	pgdb "github.com/gasmod/gas/auth/internal/apikeystore/postgres"
	litedb "github.com/gasmod/gas/auth/internal/apikeystore/sqlite"
	"github.com/gasmod/gas/auth/internal/sqlutil"
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
	// ErrNotFound indicates that no API key has the hash asked for.
	ErrNotFound = errors.New("api key not found")
	// ErrNoDatabase indicates that the database provider has no open
	// database when Init runs.
	ErrNoDatabase = errors.New("database not initialized")
	// ErrInvalidMetadata indicates that a stored key's metadata is not a
	// JSON object or JSON null.
	ErrInvalidMetadata = errors.New("invalid stored metadata")
)

// storeName prefixes the store's errors.
const storeName = "gas/auth/apikey-store"

// KeyInfo is a stored API key record. It holds neither the key nor its
// hash.
type KeyInfo struct {
	// CreatedAt is when the record was created.
	CreatedAt time.Time
	// LastUsed is when the key last authenticated, or nil if never.
	LastUsed *time.Time
	// ExpiresAt is when the key stops being valid, or nil if never.
	ExpiresAt *time.Time
	// DeletedAt is when the key was soft-deleted, or nil if it is not.
	DeletedAt *time.Time
	// Metadata holds the values stored with the key. It is never nil.
	Metadata map[string]any
	// ID identifies the record.
	ID string
	// Subject is the user or client the record belongs to.
	Subject string
	// Name is the caller-given label of the key.
	Name string
	// KeyPrefix is the leading part of the key shown to identify it.
	KeyPrefix string
	// Scopes are the permissions granted to the key. It is never nil.
	Scopes []string
}

// InsertKeyParams are the values a new API key row is stored with.
// Metadata is JSON.
type InsertKeyParams struct {
	// CreatedAt is when the record was created.
	CreatedAt time.Time
	// ExpiresAt is when the key stops being valid, or nil if never.
	ExpiresAt *time.Time
	// ID identifies the record.
	ID string
	// Subject is the user or client the record belongs to.
	Subject string
	// Name is the caller-given label of the key.
	Name string
	// KeyHash is the SHA-256 hex digest of the key.
	KeyHash string
	// KeyPrefix is the leading part of the key shown to identify it.
	KeyPrefix string
	// Scopes are the permissions granted to the key.
	Scopes []string
	// Metadata is the JSON stored with the key.
	Metadata []byte
}

// Store keeps API keys in a SQL database, as described in the package
// documentation. It is safe for concurrent use once Init has returned.
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
		s.q = &mysqlAdapter{q: mydb.New(sqlDB)}
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

// WithTx returns a copy of the Store whose queries run in tx. The copy
// belongs to the transaction and must not outlive it.
func (s *Store) WithTx(tx *sql.Tx) *Store {
	cp := *s

	cp.q = s.q.withTx(tx)

	return &cp
}

// GetKeyByHash returns the API key with the given hash, soft-deleted or
// not, or ErrNotFound when there is none.
func (s *Store) GetKeyByHash(ctx context.Context, keyHash string) (*KeyInfo, error) {
	row, err := s.q.getKeyByHash(ctx, keyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("%s: get key: %w", storeName, err)
	}

	info, err := keyInfoFromRow(row)
	if err != nil {
		return nil, fmt.Errorf("%s: get key: %w", storeName, err)
	}

	return info, nil
}

// UpdateLastUsed sets the time a key was last used. A missing key is not
// an error.
func (s *Store) UpdateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	err := s.q.updateLastUsed(ctx, id, lastUsed)
	if err != nil {
		return fmt.Errorf("%s: update last used: %w", storeName, err)
	}

	return nil
}

// InsertKey stores a new API key.
func (s *Store) InsertKey(ctx context.Context, p InsertKeyParams) error {
	err := s.q.insertKey(ctx, &p)
	if err != nil {
		return fmt.Errorf("%s: insert key: %w", storeName, err)
	}

	return nil
}

// SoftDeleteKeyByID marks the key with the given ID deleted, unless it
// already is. The row stays: GetKeyByHash still finds it, with its
// DeletedAt set, and ListKeysBySubject returns it when asked to include
// revoked keys. A missing key is not an error.
func (s *Store) SoftDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error {
	err := s.q.softDeleteKeyByID(ctx, id, deletedAt)
	if err != nil {
		return fmt.Errorf("%s: soft delete key: %w", storeName, err)
	}

	return nil
}

// SoftDeleteKeysBySubject marks every key of a subject deleted, leaving
// the time on keys that already are.
func (s *Store) SoftDeleteKeysBySubject(
	ctx context.Context,
	subject string,
	deletedAt time.Time,
) error {
	err := s.q.softDeleteKeysBySubject(ctx, subject, deletedAt)
	if err != nil {
		return fmt.Errorf("%s: soft delete keys by subject: %w", storeName, err)
	}

	return nil
}

// HardDeleteKeyByID removes the row of the key with the given ID. A
// missing key is not an error.
func (s *Store) HardDeleteKeyByID(ctx context.Context, id string) error {
	err := s.q.hardDeleteKeyByID(ctx, id)
	if err != nil {
		return fmt.Errorf("%s: hard delete key: %w", storeName, err)
	}

	return nil
}

// HardDeleteKeysBySubject removes the rows of every key of a subject.
func (s *Store) HardDeleteKeysBySubject(ctx context.Context, subject string) error {
	err := s.q.hardDeleteKeysBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("%s: hard delete keys by subject: %w", storeName, err)
	}

	return nil
}

// ListKeysBySubject returns a subject's keys, newest first, keys with the
// same creation time ordered by ID, descending, leaving out the
// soft-deleted ones unless includeRevoked is true.
func (s *Store) ListKeysBySubject(
	ctx context.Context,
	subject string,
	includeRevoked bool,
) ([]KeyInfo, error) {
	var (
		rows []keyRow
		err  error
	)

	if includeRevoked {
		rows, err = s.q.listAllKeysBySubject(ctx, subject)
	} else {
		rows, err = s.q.listKeysBySubject(ctx, subject)
	}

	if err != nil {
		return nil, fmt.Errorf("%s: list keys: %w", storeName, err)
	}

	keys := make([]KeyInfo, len(rows))

	for i := range rows {
		info, iErr := keyInfoFromRow(&rows[i])
		if iErr != nil {
			return nil, fmt.Errorf("%s: list keys: %w", storeName, iErr)
		}

		keys[i] = *info
	}

	return keys, nil
}

// DeleteExpiredKeys removes the rows of the keys whose expiry is at or
// before cutoff, the keys that are expired at that time, soft-deleted or
// not, and returns how many it removed. Keys
// without an expiry stay.
func (s *Store) DeleteExpiredKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := s.q.deleteExpiredKeys(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("%s: delete expired keys: %w", storeName, err)
	}

	return n, nil
}

// keyInfoFromRow converts a row to a KeyInfo, decoding its JSON metadata.
// Metadata that is JSON `null` decodes to an empty map, and scopes are
// never nil. Metadata that does not decode to an object is an error
// wrapping ErrInvalidMetadata with a fixed reason: encoding/json's errors
// can quote the stored value, so they are replaced, not wrapped.
func keyInfoFromRow(row *keyRow) (*KeyInfo, error) {
	info := &KeyInfo{
		ID:        row.ID,
		Name:      row.Name,
		KeyPrefix: row.KeyPrefix,
		Scopes:    row.Scopes,
		LastUsed:  row.LastUsed,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
		DeletedAt: row.DeletedAt,
		Subject:   row.Subject,
	}

	err := json.Unmarshal(row.Metadata, &info.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: want a JSON object", ErrInvalidMetadata)
	}

	if info.Metadata == nil {
		info.Metadata = map[string]any{}
	}

	if info.Scopes == nil {
		info.Scopes = []string{}
	}

	return info, nil
}
