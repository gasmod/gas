package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	mydb "github.com/gasmod/gas/auth/apikey/db/mysql"
	pgdb "github.com/gasmod/gas/auth/apikey/db/postgres"
	litedb "github.com/gasmod/gas/auth/apikey/db/sqlite"
)

//go:embed postgres/*.up.sql postgres/*.down.sql
var migrationsPostgres embed.FS

//go:embed mysql/*.up.sql mysql/*.down.sql
var migrationsMySQL embed.FS

//go:embed sqlite/*.up.sql sqlite/*.down.sql
var migrationsSQLite embed.FS

const serviceName = "gas/auth/apikey"

// KeyInfo contains non-sensitive information about an API key.
type KeyInfo struct {
	CreatedAt time.Time
	LastUsed  *time.Time
	ExpiresAt *time.Time
	DeletedAt *time.Time
	Metadata  map[string]any
	ID        string
	Subject   string
	KeyHash   string
	Name      string
	KeyPrefix string
	Scopes    []string
}

// Store is a database-backed API key store. It delegates to sqlc-generated
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

	switch s.db.Driver() {
	case "postgres", "pgx":
		if err := s.migMgr.RegisterFS(serviceName, migrationsPostgres); err != nil {
			return fmt.Errorf("%s: register migrations: %w", serviceName, err)
		}
		s.q = newPostgresAdapter(pgdb.New(sqlDB))
	case "mysql":
		if err := s.migMgr.RegisterFS(serviceName, migrationsMySQL); err != nil {
			return fmt.Errorf("%s: register migrations: %w", serviceName, err)
		}
		s.q = newMySQLAdapter(mydb.New(sqlDB))
	case "sqlite", "sqlite3":
		if err := s.migMgr.RegisterFS(serviceName, migrationsSQLite); err != nil {
			return fmt.Errorf("%s: register migrations: %w", serviceName, err)
		}
		s.q = newSQLiteAdapter(litedb.New(sqlDB))
	default:
		return fmt.Errorf("%s: unsupported driver: %q", serviceName, s.db.Driver())
	}

	return nil
}

// WithTx returns a Store whose queries execute against tx. The returned
// Store is scoped to the lifetime of tx and must not be cached.
func (s *Store) WithTx(tx *sql.Tx) *Store {
	cp := *s
	cp.q = s.q.withTx(tx)
	return &cp
}

// GetKeyByHash retrieves an API key record by its hash.
func (s *Store) GetKeyByHash(ctx context.Context, keyHash string) (*KeyInfo, error) {
	row, err := s.q.getKeyByHash(ctx, keyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, auth.ErrUnauthenticated
		}
		return nil, fmt.Errorf("%s: get key: %w", serviceName, err)
	}

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
		KeyHash:   row.KeyHash,
	}
	if jsErr := json.Unmarshal(row.Metadata, &info.Metadata); jsErr != nil {
		return nil, fmt.Errorf("%s: unmarshal metadata: %w", serviceName, jsErr)
	}

	return info, nil
}

// UpdateLastUsed updates the last_used timestamp for a key.
func (s *Store) UpdateLastUsed(ctx context.Context, id string, lastUsed time.Time) error {
	if err := s.q.updateLastUsed(ctx, id, lastUsed); err != nil {
		return fmt.Errorf("%s: update last used: %w", serviceName, err)
	}
	return nil
}

// InsertKey stores a new API key record.
func (s *Store) InsertKey(ctx context.Context, p InsertKeyParams) error {
	if err := s.q.insertKey(ctx, p); err != nil {
		return fmt.Errorf("%s: insert key: %w", serviceName, err)
	}
	return nil
}

// SoftDeleteKeyByID marks an API key as deleted by ID. Rows stay in the
// database but are excluded from authentication and listing.
func (s *Store) SoftDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error {
	if err := s.q.softDeleteKeyByID(ctx, id, deletedAt); err != nil {
		return fmt.Errorf("%s: soft delete key: %w", serviceName, err)
	}
	return nil
}

// SoftDeleteKeysBySubject marks all API keys for a subject as deleted.
func (s *Store) SoftDeleteKeysBySubject(ctx context.Context, subject string, deletedAt time.Time) error {
	if err := s.q.softDeleteKeysBySubject(ctx, subject, deletedAt); err != nil {
		return fmt.Errorf("%s: soft delete keys by subject: %w", serviceName, err)
	}
	return nil
}

// HardDeleteKeyByID permanently removes an API key row by ID.
func (s *Store) HardDeleteKeyByID(ctx context.Context, id string) error {
	if err := s.q.hardDeleteKeyByID(ctx, id); err != nil {
		return fmt.Errorf("%s: hard delete key: %w", serviceName, err)
	}
	return nil
}

// HardDeleteKeysBySubject permanently removes all API key rows for a subject.
func (s *Store) HardDeleteKeysBySubject(ctx context.Context, subject string) error {
	if err := s.q.hardDeleteKeysBySubject(ctx, subject); err != nil {
		return fmt.Errorf("%s: hard delete keys by subject: %w", serviceName, err)
	}
	return nil
}

// ListKeysBySubject returns non-sensitive info about keys for a subject. When
// includeRevoked is true, soft-deleted keys are included in the result.
func (s *Store) ListKeysBySubject(ctx context.Context, subject string, includeRevoked bool) ([]KeyInfo, error) {
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
		return nil, fmt.Errorf("%s: list keys: %w", serviceName, err)
	}
	keys := make([]KeyInfo, len(rows))
	for i := range rows {
		keys[i] = KeyInfo{
			ID:        rows[i].ID,
			Name:      rows[i].Name,
			KeyPrefix: rows[i].KeyPrefix,
			Scopes:    rows[i].Scopes,
			LastUsed:  rows[i].LastUsed,
			ExpiresAt: rows[i].ExpiresAt,
			CreatedAt: rows[i].CreatedAt,
			DeletedAt: rows[i].DeletedAt,
			Subject:   rows[i].Subject,
			KeyHash:   rows[i].KeyHash,
		}
		if jsErr := json.Unmarshal(rows[i].Metadata, &keys[i].Metadata); jsErr != nil {
			return nil, fmt.Errorf("%s: unmarshal metadata: %w", serviceName, jsErr)
		}
	}
	return keys, nil
}
