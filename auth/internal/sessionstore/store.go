package sessionstore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/gasmod/gas"
	mydb "github.com/gasmod/gas/auth/internal/sessionstore/mysql"
	pgdb "github.com/gasmod/gas/auth/internal/sessionstore/postgres"
	litedb "github.com/gasmod/gas/auth/internal/sessionstore/sqlite"
	"github.com/gasmod/gas/auth/internal/sqlutil"
)

// migrations holds every dialect's migrations, one directory per dialect.
//
//go:embed postgres/*.up.sql postgres/*.down.sql
//go:embed mysql/*.up.sql mysql/*.down.sql
//go:embed sqlite/*.up.sql sqlite/*.down.sql
var migrations embed.FS

var (
	// ErrNotFound indicates that no session has the ID hash asked for.
	ErrNotFound = errors.New("session not found")
	// ErrNoDatabase indicates that the database provider has no open
	// database when Init runs.
	ErrNoDatabase = errors.New("database not initialized")

	// errMetadataNotJSON replaces the error of encoding/json when metadata
	// cannot be encoded, since it can quote a value of the metadata.
	errMetadataNotJSON = errors.New("cannot be encoded as JSON")
	// errInvalidMetadata replaces the error of encoding/json when stored
	// metadata cannot be decoded, since it can quote the stored text.
	errInvalidMetadata = errors.New("not a JSON object")
)

// storeName prefixes the store's errors.
const storeName = "gas/auth/session-store"

// Session is a stored session, as described in the package documentation.
// It holds the hash of the session's ID, never the ID.
type Session struct {
	// CreatedAt is when the session was created.
	CreatedAt time.Time
	// ExpiresAt is when the session stops being valid.
	ExpiresAt time.Time
	// LastActive is when the session's expiry was last moved.
	LastActive time.Time
	// Metadata holds the values stored with the session.
	Metadata gas.BasePrincipalMetadata
	// IDHash is the SHA-256 hex of the session's ID.
	IDHash string
	// Subject is the user or client the session belongs to.
	Subject string
	// IPAddress is the client address the session was created from.
	IPAddress string
	// UserAgent is the User-Agent the session was created with.
	UserAgent string
}

// Store keeps sessions in a SQL database, as described in the package
// documentation. It is safe for concurrent use once Init has returned.
type Store struct {
	// db is the database provider Init reads the database from.
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
// the dialect's migrations, owned by service. A nil database, or an
// unknown driver, is an error that starts with the service's name and
// wraps ErrNoDatabase or sqlutil.ErrUnsupportedDriver.
func (s *Store) Init(service gas.Service) error {
	sqlDB := s.db.DB()
	if sqlDB == nil {
		return fmt.Errorf("%s: %w", service.Name(), ErrNoDatabase)
	}

	dialect, err := sqlutil.DialectOf(s.db.Driver())
	if err != nil {
		return fmt.Errorf("%s: %w", service.Name(), err)
	}

	switch dialect {
	case sqlutil.Postgres:
		s.q = &postgresAdapter{q: pgdb.New(sqlDB)}
	case sqlutil.MySQL:
		s.q = &mysqlAdapter{q: mydb.New(sqlDB)}
	case sqlutil.SQLite:
		s.q = &sqliteAdapter{q: litedb.New(sqlDB)}
	}

	dir, err := fs.Sub(migrations, dialect.String())
	if err != nil {
		return fmt.Errorf("%s: migrations: %w", service.Name(), err)
	}

	err = s.migMgr.RegisterFS(service, dir)
	if err != nil {
		return fmt.Errorf("%s: register migrations: %w", service.Name(), err)
	}

	return nil
}

// GetSession returns the session whose ID hashes to idHash, or
// ErrNotFound, unwrapped, when there is none.
func (s *Store) GetSession(ctx context.Context, idHash string) (*Session, error) {
	row, err := s.q.getSession(ctx, idHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("%s: get session: %w", storeName, err)
	}

	sess, err := fromRow(row)
	if err != nil {
		return nil, fmt.Errorf("%s: get session: %w", storeName, err)
	}

	return sess, nil
}

// ListSessionsBySubject returns every stored session of subject, expired
// ones included, newest first; sessions created at the same stored time
// are ordered by ID hash, descending. A subject without sessions gets an
// empty slice.
func (s *Store) ListSessionsBySubject(ctx context.Context, subject string) ([]Session, error) {
	rows, err := s.q.listSessionsBySubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("%s: list sessions by subject: %w", storeName, err)
	}

	out := make([]Session, len(rows))

	for i, row := range rows {
		sess, err := fromRow(row)
		if err != nil {
			return nil, fmt.Errorf("%s: list sessions by subject: %w", storeName, err)
		}

		out[i] = *sess
	}

	return out, nil
}

// InsertSession stores a new session.
func (s *Store) InsertSession(ctx context.Context, sess *Session) error {
	row, err := toRow(sess)
	if err != nil {
		return fmt.Errorf("%s: insert session: %w", storeName, err)
	}

	err = s.q.insertSession(ctx, row)
	if err != nil {
		return fmt.Errorf("%s: insert session: %w", storeName, err)
	}

	return nil
}

// RotateSession moves the session whose ID hashes to oldIDHash to
// sess.IDHash, and sets its address, User-Agent, expiry and last active
// time to sess's, in one UPDATE; its subject, metadata and creation time
// stay. When no session has oldIDHash it changes nothing and returns
// ErrNotFound, unwrapped, so of two calls that rotate the same session
// only one succeeds.
//
// The session keeps its row, so a concurrent delete by subject, which
// waits for the row and then checks it again, deletes the rotated session
// too. A delete and insert would leave the new row out of that delete's
// view on Postgres.
func (s *Store) RotateSession(ctx context.Context, oldIDHash string, sess *Session) error {
	n, err := s.q.rotateSession(ctx, oldIDHash, &sessionRow{
		IDHash:     sess.IDHash,
		IPAddress:  sess.IPAddress,
		UserAgent:  sess.UserAgent,
		ExpiresAt:  sess.ExpiresAt,
		LastActive: sess.LastActive,
	})
	if err != nil {
		return fmt.Errorf("%s: rotate session: %w", storeName, err)
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

// ExtendSession sets the session's expiry and last active time. A missing
// session is not an error.
func (s *Store) ExtendSession(
	ctx context.Context,
	idHash string,
	expiresAt, lastActive time.Time,
) error {
	err := s.q.extendSession(ctx, idHash, expiresAt, lastActive)
	if err != nil {
		return fmt.Errorf("%s: extend session: %w", storeName, err)
	}

	return nil
}

// DeleteSession deletes the session whose ID hashes to idHash. A missing
// session is not an error.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	err := s.q.deleteSession(ctx, idHash)
	if err != nil {
		return fmt.Errorf("%s: delete session: %w", storeName, err)
	}

	return nil
}

// DeleteSessionOfSubject deletes the session whose ID hashes to idHash,
// only if it belongs to subject. A missing session, or one of another
// subject, is not an error.
func (s *Store) DeleteSessionOfSubject(ctx context.Context, idHash, subject string) error {
	err := s.q.deleteSessionOfSubject(ctx, idHash, subject)
	if err != nil {
		return fmt.Errorf("%s: delete session of subject: %w", storeName, err)
	}

	return nil
}

// DeleteOtherSessionsOfSubject deletes every session of subject but the
// one whose ID hashes to keepIDHash, in one DELETE. A subject with no
// other sessions is not an error.
func (s *Store) DeleteOtherSessionsOfSubject(
	ctx context.Context,
	subject, keepIDHash string,
) error {
	err := s.q.deleteOtherSessionsOfSubject(ctx, subject, keepIDHash)
	if err != nil {
		return fmt.Errorf("%s: delete other sessions of subject: %w", storeName, err)
	}

	return nil
}

// DeleteSessionsBySubject deletes every session of a subject.
func (s *Store) DeleteSessionsBySubject(ctx context.Context, subject string) error {
	err := s.q.deleteSessionsBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("%s: delete sessions by subject: %w", storeName, err)
	}

	return nil
}

// DeleteExpiredSessions deletes the sessions whose expiry is at or before
// expiryCutoff or whose creation time is at or before creationCutoff, the
// sessions that are expired at those times, and returns how many it
// deleted.
func (s *Store) DeleteExpiredSessions(
	ctx context.Context,
	expiryCutoff, creationCutoff time.Time,
) (int64, error) {
	n, err := s.q.deleteExpiredSessions(ctx, expiryCutoff, creationCutoff)
	if err != nil {
		return 0, fmt.Errorf("%s: delete expired sessions: %w", storeName, err)
	}

	return n, nil
}

// fromRow returns a row as a Session, decoding its metadata. Stored JSON
// null reads as an empty map.
func fromRow(row *sessionRow) (*Session, error) {
	meta := gas.BasePrincipalMetadata{}

	err := json.Unmarshal([]byte(row.Metadata), &meta)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", errInvalidMetadata)
	}

	// JSON null leaves the map nil.
	if meta == nil {
		meta = gas.BasePrincipalMetadata{}
	}

	return &Session{
		IDHash:     row.IDHash,
		Subject:    row.Subject,
		Metadata:   meta,
		IPAddress:  row.IPAddress,
		UserAgent:  row.UserAgent,
		CreatedAt:  row.CreatedAt,
		ExpiresAt:  row.ExpiresAt,
		LastActive: row.LastActive,
	}, nil
}

// toRow returns sess as a row, with its metadata as JSON. A nil metadata
// is stored as an empty object.
func toRow(sess *Session) (*sessionRow, error) {
	meta := sess.Metadata
	if meta == nil {
		meta = gas.BasePrincipalMetadata{}
	}

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", errMetadataNotJSON)
	}

	return &sessionRow{
		IDHash:     sess.IDHash,
		Subject:    sess.Subject,
		Metadata:   string(metaJSON),
		IPAddress:  sess.IPAddress,
		UserAgent:  sess.UserAgent,
		CreatedAt:  sess.CreatedAt,
		ExpiresAt:  sess.ExpiresAt,
		LastActive: sess.LastActive,
	}, nil
}
