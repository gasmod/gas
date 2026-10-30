package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"
)

// ServiceName is the name of the migrate service, returned by Service.Name
// and prefixed to every error the service returns.
const ServiceName = "gas/migrate"

// TableName is the name of the tracking table, as described in the
// package documentation.
const TableName = "__gas_migrations"

var (
	// ErrNilDatabase indicates that the constructor was given a nil
	// gas.DatabaseProvider, or that its DB returned nil in Init.
	ErrNilDatabase = errors.New("database is nil")
	// ErrNilLogger indicates that the constructor was given a nil logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNotInitialized indicates that a method was called before Init
	// succeeded, after a failed Init, or after Close.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a service
	// that is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrUnsupportedDriver indicates that the database reports a driver
	// the service has no SQL for.
	ErrUnsupportedDriver = errors.New("unsupported driver")
	// ErrInvalidLockTimeout indicates that WithLockTimeout was given a
	// duration that is not positive.
	ErrInvalidLockTimeout = errors.New("invalid lock timeout")
	// ErrNilService indicates that a migration was registered with a nil
	// owner service.
	ErrNilService = errors.New("service is nil")
	// ErrInvalidVersion indicates that a registered version is empty, or
	// that all-digit versions and other versions are registered together.
	ErrInvalidVersion = errors.New("invalid version")
	// ErrDuplicateVersion indicates that a version is registered by two
	// services, or twice by one service with different content.
	ErrDuplicateVersion = errors.New("duplicate version")
	// ErrUnknownDirective indicates that a migration's SQL opens with a
	// `-- gas:` comment that names no directive.
	ErrUnknownDirective = errors.New("unknown directive")
	// ErrUnsupportedDirective indicates that a migration's SQL opens with
	// a directive the database's dialect does not support:
	// `-- gas:no-transaction` on SQLite.
	ErrUnsupportedDirective = errors.New("unsupported directive")
	// ErrNilFS indicates that RegisterFS was given a nil file system.
	ErrNilFS = errors.New("file system is nil")
	// ErrInvalidMigrationFile indicates that RegisterFS found a file name
	// or a set of files it cannot register.
	ErrInvalidMigrationFile = errors.New("invalid migration file")
	// ErrDirty indicates that a migration is marked dirty: it failed, and
	// on MySQL it may have left part of its changes behind. Nothing runs
	// until ClearDirty removes the mark.
	ErrDirty = errors.New("dirty migrations")
	// ErrNotDirty indicates that ClearDirty was given a version that is
	// not marked dirty.
	ErrNotDirty = errors.New("migration not dirty")
	// ErrPending indicates that registered migrations are not applied.
	ErrPending = errors.New("pending migrations")
	// ErrUnregisteredVersion indicates that Down would revert an applied
	// version that no service registers.
	ErrUnregisteredVersion = errors.New("version not registered")
	// ErrNoDownSQL indicates that Down would revert a migration that has
	// no down SQL.
	ErrNoDownSQL = errors.New("no down SQL")
	// ErrMigrationFailed indicates that a migration's SQL, or recording
	// it, failed.
	ErrMigrationFailed = errors.New("migration failed")
	// ErrTrackingRowLost indicates that a write to the tracking table that
	// must change one row, such as marking an applied migration clean,
	// changed none: something outside the service changed the row while
	// the migration ran.
	ErrTrackingRowLost = errors.New("tracking row lost")
	// ErrLockTimeout indicates that the migration lock was not acquired
	// within the lock timeout.
	ErrLockTimeout = errors.New("lock timed out")
)

// Service applies and reverts the migrations the app's services register,
// as described in the package documentation. It is safe for concurrent
// use.
type Service struct {
	// db is the database the migrations run on.
	db gas.DatabaseProvider
	// logger receives the service's events, with `service` set.
	logger gas.Logger
	// safe is the provider's SafeError, or nil.
	safe func(error) error
	// state is what Init resolved, or nil before Init succeeds, after a
	// failed Init and after Close.
	state atomic.Pointer[state]
	// reg holds the registered migrations, which outlive Close.
	reg registry
	// lockTimeout bounds each wait for the migration lock.
	lockTimeout time.Duration
	// mu makes Init and Close run one at a time.
	mu sync.Mutex
}

var (
	_ gas.Service          = (*Service)(nil)
	_ gas.MigrationManager = (*Service)(nil)
	_ gas.ReadyReporter    = (*Service)(nil)
)

// safeErrorer is a database provider that makes a driver error safe to
// return, as gas/database's Service does.
type safeErrorer interface {
	// SafeError returns err, or an error in its place that holds no
	// connection details.
	SafeError(err error) error
}

// state is what one Init resolved. It never changes once published.
type state struct {
	// db is the provider's handle, taken in Init.
	db *sql.DB
	// dialect is the SQL the database speaks.
	dialect *dialect
}

// AppliedMigration is a row of the tracking table, as Applied returns it.
type AppliedMigration struct {
	// Version is the migration's version.
	Version string
	// Service is the name of the service that registered it.
	Service string
	// Description is the migration's description.
	Description string
	// Dirty reports whether the migration is marked dirty.
	Dirty bool
}

// New returns the constructor of a Service with opts applied, in the shape
// the gas container expects. The constructor returns an error wrapping
// ErrNilDatabase for a nil provider and ErrNilLogger for a nil logger.
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger) (*Service, error) {
	return func(db gas.DatabaseProvider, logger gas.Logger) (*Service, error) {
		if isNil(db) {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilDatabase)
		}

		if isNil(logger) {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilLogger)
		}

		s := &Service{
			db:          db,
			logger:      logger.With().Str("service", ServiceName).Logger(),
			lockTimeout: DefaultLockTimeout,
		}

		if se, ok := db.(safeErrorer); ok {
			s.safe = se.SafeError
		}

		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves the dialect from the provider's driver and creates the
// tracking table under the migration lock, as described in the package
// documentation. On an initialized service it returns
// ErrAlreadyInitialized and changes nothing. A failed Init leaves the
// service uninitialized.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Load() != nil {
		return fmt.Errorf("%s: %w", ServiceName, ErrAlreadyInitialized)
	}

	if s.lockTimeout <= 0 {
		return fmt.Errorf("%s: %w: must be positive", ServiceName, ErrInvalidLockTimeout)
	}

	d, err := dialectFor(s.db.Driver())
	if err != nil {
		return err
	}

	db := s.db.DB()
	if db == nil {
		return fmt.Errorf("%s: %w", ServiceName, ErrNilDatabase)
	}

	st := &state{db: db, dialect: d}

	err = s.locked(ctx, st, func(sess *session) error {
		writeErr := s.write(ctx, sess, func(ctx context.Context, x runner) error {
			_, execErr := x.ExecContext(ctx, d.create)

			return s.safeErr(execErr)
		})
		if writeErr != nil {
			return fmt.Errorf("%s: create tracking table: %w", ServiceName, writeErr)
		}

		return nil
	})
	if err != nil {
		return err
	}

	s.state.Store(st)

	s.logger.Info("service initialized").Str("dialect", d.name).Send()

	return nil
}

// Close makes the service uninitialized. The registered migrations stay,
// so Init may run again. A run in progress goes on with the state it
// started with. Close on a service that is not initialized returns nil.
func (s *Service) Close(context.Context) error {
	s.mu.Lock()

	st := s.state.Swap(nil)

	s.mu.Unlock()

	if st != nil {
		s.logger.Info("service closed").Send()
	}

	return nil
}

// CheckReady reports readiness, as described in the package
// documentation: an error when the service is not initialized, when a
// registration failed, when a migration is dirty, unless another session
// holds the migration lock, or when registered migrations are pending.
func (s *Service) CheckReady(ctx context.Context) error {
	st, err := s.loaded()
	if err != nil {
		return err
	}

	regs, err := s.reg.snapshotFor(st.dialect)
	if err != nil {
		return err
	}

	rows, err := s.readRows(ctx, st.db, st.dialect)
	if err != nil {
		return err
	}

	err = s.dirtyUnlessInFlight(ctx, st, rows)
	if err != nil {
		return err
	}

	pending := len(pendingOf(regs, rows))
	if pending > 0 {
		return fmt.Errorf("%s: %w: %d", ServiceName, ErrPending, pending)
	}

	return nil
}

// Applied returns the rows of the tracking table in version order, as
// described in the package documentation. It takes no lock.
func (s *Service) Applied(ctx context.Context) ([]AppliedMigration, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, err
	}

	return s.readRows(ctx, st.db, st.dialect)
}

// dirtyUnlessInFlight returns dirtyError of rows, or nil when the dirty
// rows are taken as in flight: another session holds the migration lock,
// so a run may have written them ahead of its migration, which it is
// running. That is logged at debug level with the dirty versions. A
// failure to read the lock is returned.
func (s *Service) dirtyUnlessInFlight(
	ctx context.Context,
	st *state,
	rows []AppliedMigration,
) error {
	err := dirtyError(rows)
	if err == nil || st.dialect.lock == nil {
		return err
	}

	held, lockErr := st.dialect.lock.held(ctx, st.db)
	if lockErr != nil {
		return fmt.Errorf("%s: read migration lock: %w", ServiceName, s.safeErr(lockErr))
	}

	if !held {
		return err
	}

	s.logger.Debug("dirty migrations in flight").
		Str("versions", strings.Join(dirtyVersions(rows), ", ")).
		Send()

	return nil
}

// loaded returns the state of the last successful Init, or an error
// wrapping ErrNotInitialized.
func (s *Service) loaded() (*state, error) {
	st := s.state.Load()
	if st == nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, ErrNotInitialized)
	}

	return st, nil
}

// safeErr passes a driver error through the provider's SafeError, when it
// has one.
func (s *Service) safeErr(err error) error {
	if s.safe == nil {
		return err
	}

	return s.safe(err)
}

// queryer runs a query, as *sql.DB, *sql.Conn and *sql.Tx do.
type queryer interface {
	// QueryContext runs a query that returns rows.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readRows returns the rows of the tracking table, read with q, in version
// order.
func (s *Service) readRows(
	ctx context.Context,
	q queryer,
	d *dialect,
) (out []AppliedMigration, err error) {
	rows, err := q.QueryContext(ctx, d.selectRows)
	if err != nil {
		return nil, fmt.Errorf("%s: read tracking table: %w", ServiceName, s.safeErr(err))
	}

	defer func() {
		closeErr := rows.Close()
		if err == nil && closeErr != nil {
			out, err = nil, fmt.Errorf(
				"%s: read tracking table: %w",
				ServiceName,
				s.safeErr(closeErr),
			)
		}
	}()

	for rows.Next() {
		var m AppliedMigration

		err = rows.Scan(&m.Version, &m.Service, &m.Description, &m.Dirty)
		if err != nil {
			return nil, fmt.Errorf("%s: read tracking table: %w", ServiceName, s.safeErr(err))
		}

		out = append(out, m)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("%s: read tracking table: %w", ServiceName, s.safeErr(err))
	}

	slices.SortFunc(out, func(a, b AppliedMigration) int {
		return compareVersions(a.Version, b.Version)
	})

	return out, nil
}

// dirtyError returns an error wrapping ErrDirty that lists the dirty
// versions of rows, or nil when none is dirty.
func dirtyError(rows []AppliedMigration) error {
	dirty := dirtyVersions(rows)
	if len(dirty) == 0 {
		return nil
	}

	return fmt.Errorf("%s: %w: %s", ServiceName, ErrDirty, strings.Join(dirty, ", "))
}

// dirtyVersions returns the versions of the dirty rows of rows, in their
// order.
func dirtyVersions(rows []AppliedMigration) []string {
	var dirty []string

	for _, r := range rows {
		if r.Dirty {
			dirty = append(dirty, r.Version)
		}
	}

	return dirty
}

// pendingOf returns the registered migrations of regs that have no row in
// rows, in version order.
func pendingOf(regs []*registered, rows []AppliedMigration) []*registered {
	applied := make(map[string]struct{}, len(rows))

	for _, r := range rows {
		applied[r.Version] = struct{}{}
	}

	var out []*registered

	for _, r := range regs {
		if _, ok := applied[r.mig.Version]; !ok {
			out = append(out, r)
		}
	}

	return out
}

// isNil reports whether v is nil, or an interface holding a nil pointer,
// map, slice, func or chan.
func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	//nolint:exhaustive // only these kinds can be nil.
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}
