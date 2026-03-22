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

// CacheProvider abstracts key-value caching. Implemented by in-memory,
// Redis, Valkey, or any other cache service.
type CacheProvider interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// JobQueueProvider abstracts async job/message queue processing.
// Implemented by gas-queue-sqs or any other queue service. The interface
// is pull-based: consumers call Dequeue in their own worker loop and
// acknowledge results with Ack/Nack.
type JobQueueProvider interface {
	Enqueue(ctx context.Context, queue string, payload []byte, opts ...EnqueueOption) error
	Dequeue(ctx context.Context, queue string, maxMessages int, wait time.Duration) ([]Job, error)
	Ack(ctx context.Context, queue string, job Job) error
	Nack(ctx context.Context, queue string, job Job) error
}

// Job represents a message received from a queue.
type Job struct {
	ID            string
	ReceiptHandle string            // opaque token used by Ack/Nack
	Attributes    map[string]string // provider-specific metadata
	Body          []byte
}

// EnqueueOption configures an Enqueue call.
type EnqueueOption func(*enqueueOptions)

type enqueueOptions struct {
	Attributes map[string]string
	GroupID    string // FIFO ordering (SQS: MessageGroupId)
	DedupeID   string // deduplication (SQS: MessageDeduplicationId)
	Delay      time.Duration
}

// WithDelay sets an initial delay before the job becomes visible to consumers.
func WithDelay(d time.Duration) EnqueueOption {
	return func(o *enqueueOptions) { o.Delay = d }
}

// WithGroupID sets the message group for FIFO queue ordering.
func WithGroupID(id string) EnqueueOption {
	return func(o *enqueueOptions) { o.GroupID = id }
}

// WithDedupeID sets a deduplication identifier.
func WithDedupeID(id string) EnqueueOption {
	return func(o *enqueueOptions) { o.DedupeID = id }
}

// WithJobAttributes attaches provider-specific key-value metadata to the job.
func WithJobAttributes(attrs map[string]string) EnqueueOption {
	return func(o *enqueueOptions) { o.Attributes = attrs }
}

// ApplyEnqueueOptions resolves variadic EnqueueOption values into a concrete
// enqueueOptions struct. Implementations call this inside their Enqueue method.
func ApplyEnqueueOptions(opts []EnqueueOption) (delay time.Duration, groupID, dedupeID string, attrs map[string]string) {
	o := &enqueueOptions{}
	for _, fn := range opts {
		fn(o)
	}
	return o.Delay, o.GroupID, o.DedupeID, o.Attributes
}

// EmailProvider abstracts email sending.
type EmailProvider interface {
	Send(ctx context.Context, msg *Email) error
	SendFromTemplate(ctx context.Context, msg *TemplatedEmail) error
}

// Email represents an email message.
type Email struct {
	From     string
	ReplyTo  string
	Subject  string
	TextBody string
	HTMLBody string

	Headers map[string]string

	To  []string
	Cc  []string
	Bcc []string
}

// TemplatedEmail represents an email message where templates and data define subject, text, and HTML bodies.
type TemplatedEmail struct {
	SubjectTemplate string
	TextTemplate    string
	HTMLTemplate    string
	Data            any
	Email
}

// StorageProvider abstracts file storage (S3, DO Spaces, local filesystem, etc.).
type StorageProvider interface {
	Upload(ctx context.Context, key string, data io.Reader) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignURL(ctx context.Context, key string, expires time.Duration) (string, error)
}

// TemplateProvider abstracts template storage and retrieval. Implementations
// may be backed by a filesystem, database, memory, or any combination.
// Consumers such as UIProvider (HTML rendering) and EmailProvider (email
// templating) resolve raw template content through this interface.
type TemplateProvider interface {
	// Get returns the raw template content by name.
	Get(name string) ([]byte, error)

	// List returns all available template names.
	List() ([]string, error)

	// Register adds or replaces a template by name and raw content.
	Register(name string, content []byte)

	// RegisterFS walks an fs.FS and registers every template file found.
	RegisterFS(fsys fs.FS) error
}

// UIProvider abstracts template rendering. Implemented by gas-ui or any
// other UI service. Template storage and retrieval is handled by
// TemplateProvider; UIProvider focuses on compilation and rendering.
type UIProvider interface {
	Render(w http.ResponseWriter, name string, data any) error
	RenderFragment(w http.ResponseWriter, name string, data any) error // renders without layout wrapper, useful for HTMX
	RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error
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
