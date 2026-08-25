package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	mydb "github.com/gasmod/gas/auth/session/db/mysql"
	pgdb "github.com/gasmod/gas/auth/session/db/postgres"
	litedb "github.com/gasmod/gas/auth/session/db/sqlite"
)

//go:embed postgres/20250323001_create_sessions.up.sql
var migrationUpPostgres string

//go:embed postgres/20250323001_create_sessions.down.sql
var migrationDownPostgres string

//go:embed mysql/20250323001_create_sessions.up.sql
var migrationUpMySQL string

//go:embed mysql/20250323001_create_sessions.down.sql
var migrationDownMySQL string

//go:embed sqlite/20250323001_create_sessions.up.sql
var migrationUpSQLite string

//go:embed sqlite/20250323001_create_sessions.down.sql
var migrationDownSQLite string

const serviceName = "gas/auth/session-store"

// Session represents a stored session record.
type Session struct {
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastActive time.Time
	Metadata   gas.BasePrincipalMetadata
	ID         string
	Subject    string
	IPAddress  string
	UserAgent  string
}

// Store is a database-backed session store. It delegates to sqlc-generated
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

	var up, down string

	switch s.db.Driver() {
	case "postgres", "pgx":
		up = migrationUpPostgres
		down = migrationDownPostgres
		s.q = newPostgresAdapter(pgdb.New(sqlDB))
	case "mysql":
		up = migrationUpMySQL
		down = migrationDownMySQL
		s.q = newMySQLAdapter(mydb.New(sqlDB))
	case "sqlite", "sqlite3":
		up = migrationUpSQLite
		down = migrationDownSQLite
		s.q = newSQLiteAdapter(litedb.New(sqlDB))
	default:
		return fmt.Errorf("%s: unsupported driver: %q", serviceName, s.db.Driver())
	}

	s.migMgr.Register(serviceName, gas.Migration{
		Version:     "20250323001",
		Description: "create sessions table",
		Up:          up,
		Down:        down,
	})

	return nil
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	row, err := s.q.getSession(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, auth.ErrUnauthenticated
		}
		return nil, fmt.Errorf("%s: get session: %w", serviceName, err)
	}

	meta := gas.BasePrincipalMetadata{}
	if row.Metadata != "" {
		if jsErr := json.Unmarshal([]byte(row.Metadata), &meta); jsErr != nil {
			return nil, fmt.Errorf("%s: unmarshal metadata: %w", serviceName, jsErr)
		}
	}

	return &Session{
		ID:         row.ID,
		Subject:    row.Subject,
		Metadata:   meta,
		IPAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		CreatedAt:  row.CreatedAt,
		ExpiresAt:  row.ExpiresAt,
		LastActive: row.LastActive,
	}, nil
}

// InsertSession stores a new session.
func (s *Store) InsertSession(ctx context.Context, sess *Session) error {
	metaJSON, err := json.Marshal(sess.Metadata)
	if err != nil {
		return fmt.Errorf("%s: marshal metadata: %w", serviceName, err)
	}

	if err := s.q.insertSession(ctx, sess.ID, sess.Subject, string(metaJSON), sess.IPAddress, sess.UserAgent, sess.CreatedAt, sess.ExpiresAt, sess.LastActive); err != nil {
		return fmt.Errorf("%s: insert session: %w", serviceName, err)
	}
	return nil
}

// ExtendSession updates the expiry and last active time.
func (s *Store) ExtendSession(ctx context.Context, id string, expiresAt, lastActive time.Time) error {
	if err := s.q.extendSession(ctx, id, expiresAt, lastActive); err != nil {
		return fmt.Errorf("%s: extend session: %w", serviceName, err)
	}
	return nil
}

// DeleteSession deletes a session by ID.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if err := s.q.deleteSession(ctx, id); err != nil {
		return fmt.Errorf("%s: delete session: %w", serviceName, err)
	}
	return nil
}

// DeleteSessionsBySubject deletes all sessions for a subject.
func (s *Store) DeleteSessionsBySubject(ctx context.Context, subject string) error {
	if err := s.q.deleteSessionsBySubject(ctx, subject); err != nil {
		return fmt.Errorf("%s: delete sessions by subject: %w", serviceName, err)
	}
	return nil
}

// DeleteExpiredSessions deletes sessions that expired before the given time.
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.deleteExpiredSessions(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("%s: delete expired sessions: %w", serviceName, err)
	}
	return n, nil
}
