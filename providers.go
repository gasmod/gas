package gas

import (
	"context"
	"database/sql"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"time"

	config "github.com/gasmod/gas-config"
)

// DatabaseProvider abstracts database access. Implemented by gas-database
// or any other database service. DB() exposes the underlying *sql.DB so
// that sqlc-generated code and transactions can use it directly.
type DatabaseProvider interface {
	DB() *sql.DB
	Driver() string
	Ping(ctx context.Context) error
	Query(ctx context.Context, query string, args ...any) (Rows, error)
	Exec(ctx context.Context, query string, args ...any) (Result, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) (err error)
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
	Send(ctx context.Context, msg *Email) error
	SendFromTemplate(ctx context.Context, msg *TemplatedEmail) error
}

// Email represents an email message.
//
//nolint:govet // intentional field order
type Email struct {
	To       []string
	Cc       []string
	Bcc      []string
	From     string
	ReplyTo  string
	Subject  string
	TextBody string
	HTMLBody string
	Headers  map[string]string
}

// TemplatedEmail represents an email message where templates and data define subject, text, and HTML bodies.
//
//nolint:govet // intentional field order
type TemplatedEmail struct {
	Email

	SubjectTemplate string
	TextTemplate    string
	HTMLTemplate    string

	Data any
}

// StorageProvider abstracts file storage (S3, DO Spaces, local filesystem, etc.).
type StorageProvider interface {
	Upload(ctx context.Context, key string, data io.Reader) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// UIProvider abstracts template rendering. Implemented by gas-ui or any
// other UI service. Modules that need to render HTML pages accept a
// UIProvider through their functional options.
type UIProvider interface {
	Render(w http.ResponseWriter, name string, data any) error
	RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error
	RegisterTemplate(name string, content []byte)
	RegisterTemplatesFS(fsys fs.FS) error
	RegisterFuncs(funcs template.FuncMap)
}

// ConfigProvider defines the config struct API, used by other modules that needs access to the config provider.
type ConfigProvider interface {
	SetDefault(key string, value any)
	SetDefaults(values any) error
	Set(key string, value any)
	Bind(dest any, options ...config.BindOption) error
	Get(key string) any
	Find(key string) (value any, exist bool)
	Values() map[string]any
}
