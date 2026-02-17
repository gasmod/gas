package gas

import (
	"context"
	"database/sql"
	"io"
	"time"
)

// DatabaseProvider abstracts database access. Implemented by gas-database
// or any other database module. DB() exposes the underlying *sql.DB so
// that sqlc-generated code and transactions can use it directly.
type DatabaseProvider interface {
	DB() *sql.DB
	Query(ctx context.Context, query string, args ...any) (Rows, error)
	Exec(ctx context.Context, query string, args ...any) (Result, error)
}

// Rows represents the result set of a query. Mirrors the standard
// database/sql Rows interface so implementations can wrap it directly.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// Result represents the outcome of an Exec operation.
type Result interface {
	RowsAffected() (int64, error)
	LastInsertId() (int64, error)
}

// CacheProvider abstracts key-value caching.
type CacheProvider interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// EmailProvider abstracts email sending.
type EmailProvider interface {
	Send(ctx context.Context, msg Email) error
}

// Email represents an email message.
type Email struct {
	To      string
	From    string
	Subject string
	Body    string
	HTML    string
}

// StorageProvider abstracts file storage (S3, DO Spaces, local filesystem, etc.).
type StorageProvider interface {
	Upload(ctx context.Context, key string, data io.Reader) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}
