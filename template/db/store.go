package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	sqlitedriver "modernc.org/sqlite"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
	"github.com/gasmod/gas/template/internal/value"
)

// ServiceName is the name of the store, returned by Store.Name and
// recorded as the owner of its migrations.
const ServiceName = "gas/template/db"

// TableName is the table the templates are kept in, as described in the
// package documentation.
const TableName = "__gas_templates"

// DefaultNamespace is the namespace of a store not given another.
const DefaultNamespace = "default"

var (
	// ErrNilDatabase indicates that the constructor was given a nil
	// gas.DatabaseProvider, or that its DB returned nil in Init.
	ErrNilDatabase = errors.New(template.ErrorPrefix + "database is nil")
	// ErrNilLogger indicates that the constructor was given a nil logger.
	ErrNilLogger = errors.New(template.ErrorPrefix + "logger is nil")
	// ErrNilMigrations indicates that the constructor was given a nil
	// gas.MigrationManager.
	ErrNilMigrations = errors.New(template.ErrorPrefix + "migration manager is nil")
	// ErrInvalidNamespace indicates that WithNamespace was given a
	// namespace the store does not accept.
	ErrInvalidNamespace = errors.New(template.ErrorPrefix + "invalid namespace")
	// ErrUnsupportedDriver indicates that the database reports a driver
	// the store has no SQL for.
	ErrUnsupportedDriver = errors.New(template.ErrorPrefix + "unsupported driver")
	// ErrQuery indicates that a statement on the table failed. It never
	// wraps the driver's error, as described in the package
	// documentation.
	ErrQuery = errors.New(template.ErrorPrefix + "query failed")
)

// Store keeps templates in a table of a database, as described in the
// package documentation. It implements gas.Service and is safe for
// concurrent use.
type Store struct {
	// db is the database the table is in.
	db gas.DatabaseProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// migrations receives the table's migrations in Init.
	migrations gas.MigrationManager
	// safe is the provider's SafeError, or nil.
	safe func(error) error
	// conn is what Init resolved, or nil unless the store is open.
	conn *conn
	// namespace scopes the store's rows.
	namespace string
	// exts are the extensions a name must end in.
	exts []string
	// maxSize is the most bytes a template may hold.
	maxSize int64
	// mu guards conn and closed.
	mu sync.RWMutex
	// closed reports whether Close ran after a successful Init, and Init
	// did not run again since.
	closed bool
}

var (
	_ gas.TemplateProvider = (*Store)(nil)
	_ gas.Service          = (*Store)(nil)
)

// conn is what one Init resolved. It never changes once published.
type conn struct {
	// db is the provider's handle, taken in Init.
	db *sql.DB
	// dialect is the SQL the database speaks.
	dialect *dialect
}

// safeErrorer is a database provider that makes a driver error safe to
// return, as gas/database's Service does.
type safeErrorer interface {
	// SafeError returns err, or an error in its place that holds no
	// connection details.
	SafeError(err error) error
}

// Option configures a Store.
type Option func(*Store)

// WithNamespace sets the namespace the store's rows are scoped to. The
// constructor checks it, as described in the package documentation.
//
// Default: DefaultNamespace.
func WithNamespace(namespace string) Option {
	return func(s *Store) { s.namespace = namespace }
}

// WithExtensions sets the extensions a template name must end in. The
// constructor checks them, as described in the template package
// documentation.
//
// Default: template.DefaultExtension.
func WithExtensions(exts ...string) Option {
	exts = slices.Clone(exts)

	return func(s *Store) { s.exts = exts }
}

// WithMaxSize sets the most bytes a template may hold. The constructor
// checks it with template.CheckMaxSize.
//
// Default: template.DefaultMaxSize.
func WithMaxSize(n int64) Option {
	return func(s *Store) { s.maxSize = n }
}

// NewStore returns the constructor of a Store with opts applied, in the
// shape the gas container expects. The constructor returns ErrNilDatabase,
// ErrNilLogger or ErrNilMigrations for a nil dependency, and an error
// wrapping ErrInvalidNamespace, template.ErrInvalidExtension or
// template.ErrInvalidMaxSize for a bad option.
func NewStore(
	opts ...Option,
) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) (*Store, error) {
	return func(db gas.DatabaseProvider, logger gas.Logger, migrations gas.MigrationManager) (*Store, error) {
		switch {
		case value.IsNil(db):
			return nil, ErrNilDatabase
		case value.IsNil(logger):
			return nil, ErrNilLogger
		case value.IsNil(migrations):
			return nil, ErrNilMigrations
		}

		s := &Store{
			db:         db,
			logger:     logger.With().Str("service", ServiceName).Logger(),
			migrations: migrations,
			namespace:  DefaultNamespace,
			exts:       []string{template.DefaultExtension},
			maxSize:    template.DefaultMaxSize,
		}

		if se, ok := db.(safeErrorer); ok {
			s.safe = se.SafeError
		}

		for _, opt := range opts {
			opt(s)
		}

		err := checkNamespace(s.namespace)
		if err != nil {
			return nil, err
		}

		err = template.CheckExtensions(s.exts)
		if err != nil {
			return nil, err //nolint:wrapcheck // CheckExtensions returns complete errors.
		}

		err = template.CheckMaxSize(s.maxSize)
		if err != nil {
			return nil, err //nolint:wrapcheck // CheckMaxSize returns complete errors.
		}

		return s, nil
	}
}

// checkNamespace returns nil for a namespace the store accepts, and
// otherwise an error wrapping ErrInvalidNamespace that gives the reason
// and never the namespace.
func checkNamespace(namespace string) error {
	var reason string

	switch {
	case namespace == "":
		reason = "empty"
	case !utf8.ValidString(namespace):
		reason = "not valid UTF-8"
	case strings.ContainsRune(namespace, 0):
		reason = "holds a NUL byte"
	case utf8.RuneCountInString(namespace) > maxName:
		reason = fmt.Sprintf("longer than %d characters", maxName)
	default:
		return nil
	}

	return fmt.Errorf("%w: %s", ErrInvalidNamespace, reason)
}

// Name returns ServiceName.
func (*Store) Name() string { return ServiceName }

// Init picks the dialect from the provider's driver, takes the provider's
// DB and registers the table's migrations, as described in the package
// documentation. On an open store it returns
// template.ErrAlreadyInitialized and changes nothing. A failed Init leaves
// the store as it was.
func (s *Store) Init(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		return template.ErrAlreadyInitialized
	}

	d, err := dialectFor(s.db.Driver())
	if err != nil {
		return err
	}

	db := s.db.DB()
	if db == nil {
		return ErrNilDatabase
	}

	owned := slices.Clone(d.migrations)
	for i := range owned {
		owned[i].Service = s
	}

	s.migrations.RegisterSlice(s, owned)

	s.conn = &conn{db: db, dialect: d}
	s.closed = false

	s.logger.Info("store initialized").
		Str("dialect", d.name).
		Str("namespace", s.namespace).
		Send()

	return nil
}

// Close makes every later call but Init return an error wrapping
// template.ErrClosed. It closes nothing: the database belongs to its
// provider. Close on a store that is not open returns nil.
func (s *Store) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		s.conn = nil
		s.closed = true
	}

	return nil
}

// Get returns the content of the template name, as described in the
// package documentation. Its errors wrap template.ErrInvalidName,
// template.ErrTemplateNotFound, template.ErrTooLarge, ErrQuery,
// template.ErrNotInitialized or template.ErrClosed. Content over the
// store's maximum size is not sent by the database.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	c, err := s.open(name)
	if err != nil {
		return nil, err
	}

	var (
		content []byte
		size    int64
	)

	err = c.db.QueryRowContext(ctx, c.dialect.get, s.maxSize, s.namespace, name).
		Scan(&content, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	if err != nil {
		return nil, s.queryError("get", name, err)
	}

	if size > s.maxSize {
		//nolint:wrapcheck // TooLarge returns complete errors.
		return nil, template.TooLarge(name, s.maxSize)
	}

	// database/sql copies a []byte destination, so content is the
	// store's own; it is nil for an empty value on some drivers.
	if content == nil {
		content = []byte{}
	}

	return content, nil
}

// List returns the names of the namespace's templates in byte order, as
// described in the package documentation. A row whose name the store
// would refuse is left out.
func (s *Store) List(ctx context.Context) (names []string, err error) {
	c, err := s.loaded()
	if err != nil {
		return nil, err
	}

	rows, err := c.db.QueryContext(ctx, c.dialect.list, s.namespace)
	if err != nil {
		return nil, s.queryError("list", "", err)
	}

	defer func() {
		closeErr := rows.Close()
		if err == nil && closeErr != nil {
			names, err = nil, s.queryError("list", "", closeErr)
		}
	}()

	names = []string{}

	for rows.Next() {
		var name string

		err = rows.Scan(&name)
		if err != nil {
			return nil, s.queryError("list", "", err)
		}

		if s.check(name) == nil {
			names = append(names, name)
		}
	}

	err = rows.Err()
	if err != nil {
		return nil, s.queryError("list", "", err)
	}

	slices.Sort(names)

	return names, nil
}

// Register stores a copy of content as the template name, replacing one
// of that name. Nil content is stored as empty, and content over the
// store's maximum size is an error wrapping template.ErrTooLarge. Its
// errors are as Get's, without template.ErrTemplateNotFound.
func (s *Store) Register(ctx context.Context, name string, content []byte) error {
	c, err := s.open(name)
	if err != nil {
		return err
	}

	if int64(len(content)) > s.maxSize {
		//nolint:wrapcheck // TooLarge returns complete errors.
		return template.TooLarge(name, s.maxSize)
	}

	_, err = c.db.ExecContext(ctx, c.dialect.upsert, s.namespace, name, value.Clone(content))
	if err != nil {
		return s.queryError("register", name, err)
	}

	return nil
}

// RegisterFS reads the files of fsys with one of the store's extensions
// and stores them in one transaction, all or none, as described in the
// template package documentation.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if value.IsNil(fsys) {
		return template.ErrNilFS
	}

	c, err := s.loaded()
	if err != nil {
		return err
	}

	all, err := files.ReadAll(ctx, fsys, s.exts, s.maxSize)
	if err != nil {
		return err //nolint:wrapcheck // ReadAll returns complete errors.
	}

	for _, f := range all {
		err = s.check(f.Name)
		if err != nil {
			return err
		}
	}

	return s.upsertAll(ctx, c, all)
}

// Exists reports whether the template name is stored. Its errors are as
// Get's, without template.ErrTemplateNotFound.
func (s *Store) Exists(ctx context.Context, name string) (bool, error) {
	c, err := s.open(name)
	if err != nil {
		return false, err
	}

	var n int64

	err = c.db.QueryRowContext(ctx, c.dialect.count, s.namespace, name).Scan(&n)
	if err != nil {
		return false, s.queryError("exists", name, err)
	}

	return n > 0, nil
}

// Delete removes the template name. Its errors are as Get's: one wrapping
// template.ErrTemplateNotFound when there is none.
func (s *Store) Delete(ctx context.Context, name string) error {
	c, err := s.open(name)
	if err != nil {
		return err
	}

	res, err := c.db.ExecContext(ctx, c.dialect.remove, s.namespace, name)
	if err != nil {
		return s.queryError("delete", name, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return s.queryError("delete", name, err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	return nil
}

// upsertAll stores the files in one transaction, rolled back on failure.
func (s *Store) upsertAll(ctx context.Context, c *conn, all []files.File) (err error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return s.queryError("register files", "", err)
	}

	defer func() {
		if err == nil {
			return
		}

		rbErr := tx.Rollback()
		if rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, s.queryError("register files: rollback", "", rbErr))
		}
	}()

	for _, f := range all {
		_, err = tx.ExecContext(ctx, c.dialect.upsert, s.namespace, f.Name, f.Content)
		if err != nil {
			return s.queryError("register", f.Name, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return s.queryError("register files: commit", "", err)
	}

	return nil
}

// open checks name with template.CheckName and returns the state of the
// open store, checking name against the dialect's limits too.
func (s *Store) open(name string) (*conn, error) {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return nil, err //nolint:wrapcheck // CheckName returns complete errors.
	}

	c, err := s.loaded()
	if err != nil {
		return nil, err
	}

	err = tooLong(name)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// check returns the error of a name the store refuses on c's dialect.
func (s *Store) check(name string) error {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return err //nolint:wrapcheck // CheckName returns complete errors.
	}

	return tooLong(name)
}

// tooLong returns an error wrapping template.ErrInvalidName for a name
// longer than maxName characters.
func tooLong(name string) error {
	if utf8.RuneCountInString(name) > maxName {
		return fmt.Errorf("%w: longer than %d characters", template.ErrInvalidName, maxName)
	}

	return nil
}

// loaded returns the state of the open store, or
// template.ErrNotInitialized or template.ErrClosed.
func (s *Store) loaded() (*conn, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	switch {
	case s.conn != nil:
		return s.conn, nil
	case s.closed:
		return nil, template.ErrClosed
	default:
		return nil, template.ErrNotInitialized
	}
}

// queryError returns the failure of the statement op on the template
// name, or of op alone when name is empty, wrapping ErrQuery and never
// err, whose text can hold the database's user, host and name. A failure
// the provider's SafeError replaces, a connection-level one, keeps
// SafeError's text and what it wraps; a context's error or database/sql's
// ErrConnDone or ErrTxDone is kept; any other gives only the driver's
// code for it, as driverCode tells it.
func (s *Store) queryError(op, name string, err error) error {
	what := op
	if name != "" {
		what = fmt.Sprintf("%s %q", op, name)
	}

	if s.safe != nil {
		safe := s.safe(err)
		if !errors.Is(safe, err) {
			return fmt.Errorf("%w: %s: %w", ErrQuery, what, safe)
		}
	}

	for _, kept := range []error{context.Canceled, context.DeadlineExceeded, sql.ErrConnDone, sql.ErrTxDone} {
		if errors.Is(err, kept) {
			return fmt.Errorf("%w: %s: %w", ErrQuery, what, kept)
		}
	}

	return fmt.Errorf("%w: %s: %s", ErrQuery, what, driverCode(err))
}

// driverCode returns the code of a driver's error, without its message:
// MySQL's error number, PostgreSQL's SQLSTATE or SQLite's result code, and
// `driver error` for an error of another driver, or none.
func driverCode(err error) string {
	if e, ok := errors.AsType[*mysqldriver.MySQLError](err); ok {
		return fmt.Sprintf("mysql error %d", e.Number)
	}

	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if ok && sqlState(pgErr.Code) {
		return "postgres error " + pgErr.Code
	}

	if e, ok := errors.AsType[*sqlitedriver.Error](err); ok {
		return fmt.Sprintf("sqlite error %d", e.Code())
	}

	return "driver error"
}

// sqlState reports whether code is a SQLSTATE: five digits or capital
// letters.
func sqlState(code string) bool {
	return len(code) == len("42P01") &&
		strings.Trim(code, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ") == ""
}
