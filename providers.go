package gas

import (
	"context"
	"database/sql"
	"io/fs"
	"time"
)

// DatabaseProvider abstracts database access. Implemented by gas/database
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
// Implemented by gas/queue/sqs or any other queue service. The interface
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

// enqueueOptions holds what the EnqueueOption values of one call set.
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

// ApplyEnqueueOptions returns what opts set, applied in order: the delay,
// the group ID, the dedupe ID and the attributes, each the zero value when
// no option sets it. A JobQueueProvider calls it inside Enqueue.
func ApplyEnqueueOptions(
	opts []EnqueueOption,
) (delay time.Duration, groupID, dedupeID string, attrs map[string]string) {
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

// TemplatedEmail is an email message whose subject, text and HTML bodies
// are rendered from templates with Data.
type TemplatedEmail struct {
	Data            any
	SubjectTemplate string
	TextTemplate    string
	HTMLTemplate    string
	Email           //nolint:embeddedstructfieldcheck // last, as govet's fieldalignment orders it
}

// TemplateProvider abstracts template storage and retrieval. Implementations
// may be backed by a filesystem, database, memory, or any combination.
// Consumers such as the ui module (HTML rendering) and EmailProvider
// (email templating) resolve raw template content through this interface.
type TemplateProvider interface {
	// Get returns the raw template content by name.
	Get(ctx context.Context, name string) ([]byte, error)

	// List returns all available template names.
	List(ctx context.Context) ([]string, error)

	// Register adds or replaces a template by name and raw content.
	Register(ctx context.Context, name string, content []byte) error

	// RegisterFS walks an fs.FS and registers every template file found.
	RegisterFS(ctx context.Context, fsys fs.FS) error
}

// HealthReporter is implemented by services that can report their own health.
// Answers the question "am I alive?": a failure here signals that the process
// is in a broken state that a restart would resolve (lost connection that
// won't recover, corrupt internal state, deadlock). Maps to a Kubernetes
// livenessProbe. A nil return means healthy; a non-nil error describes the
// failure.
type HealthReporter interface {
	CheckHealth(ctx context.Context) error
}

// ReadyReporter is implemented by services that can report whether they are
// ready to accept traffic. Answers the question "should traffic be routed to
// me?", distinct from health, since a service can be alive but not yet ready
// (warming caches, pending migrations, dependency briefly unavailable,
// draining during shutdown). Maps to a Kubernetes readinessProbe. A nil
// return means ready; a non-nil error describes why the service is not ready.
type ReadyReporter interface {
	CheckReady(ctx context.Context) error
}

// HealthProvider aggregates health signals across all active services that
// implement HealthReporter. The returned map keys are service names; a nil
// value indicates a healthy service.
type HealthProvider interface {
	CheckHealth(ctx context.Context) map[string]error
}

// ReadyProvider aggregates readiness signals across all active services that
// implement ReadyReporter. The returned map keys are service names; a nil
// value indicates a ready service.
type ReadyProvider interface {
	CheckReady(ctx context.Context) map[string]error
}

// ConfigProvider is how services, and App.Start, read configuration, as
// described under Configuration in the package documentation. The config
// module's *config.Config implements it; core defines it so the root
// module depends on no other gas module.
type ConfigProvider interface {
	// SetDefault sets the value at key unless the configuration holds one.
	SetDefault(key string, value any) error
	// SetDefaults sets defaults from a struct or map, as SetDefault does
	// for each of its keys.
	SetDefaults(values any) error
	// Set sets the value at key.
	Set(key string, value any) error
	// Bind fills dest, a pointer to a struct, from the configuration and
	// validates it.
	Bind(dest any) error
	// Get returns the value at key, or nil.
	Get(key string) any
	// Find returns the value at key and reports whether it exists.
	Find(key string) (value any, exist bool)
	// Values returns a copy of every value.
	Values() map[string]any
}
