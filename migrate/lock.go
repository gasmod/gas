package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// lockPollInterval is the wait between two attempts to take a lock
	// that the database cannot wait for itself.
	lockPollInterval = 100 * time.Millisecond
	// releaseTimeout bounds the work that runs after a run's context has
	// ended: marking a migration dirty, rolling back, committing and
	// releasing the lock.
	releaseTimeout = 30 * time.Second
	// sqliteBusy is SQLite's SQLITE_BUSY, the primary result code of a
	// lock another connection holds.
	sqliteBusy = 5
)

// errLockNotHeld is the error of releasing a lock the session did not
// hold.
var errLockNotHeld = errors.New("lock was not held")

// errLockNull is MySQL's GET_LOCK returning NULL, an error in the server.
var errLockNull = errors.New("GET_LOCK returned NULL")

// locker takes and releases the migration lock of one dialect, on one
// connection.
type locker interface {
	// try takes the lock, waiting up to wait seconds when the database
	// can wait itself, and reports whether it holds it.
	try(ctx context.Context, conn *sql.Conn, wait int) (bool, error)
	// release releases the lock.
	release(ctx context.Context, conn *sql.Conn) error
	// waits reports whether try can wait in the database.
	waits() bool
	// held reports, through db, whether a session holds the lock.
	held(ctx context.Context, db *sql.DB) (bool, error)
}

// postgresLock is PostgreSQL's session-level advisory lock on
// PostgresLockKey. Closing the connection releases it.
type postgresLock struct{}

// try runs pg_try_advisory_lock.
func (postgresLock) try(ctx context.Context, conn *sql.Conn, _ int) (bool, error) {
	var ok bool

	err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", PostgresLockKey).Scan(&ok)

	return ok, err //nolint:wrapcheck // Service.wait wraps it.
}

// release runs pg_advisory_unlock, which reports false for a lock the
// session does not hold.
func (postgresLock) release(ctx context.Context, conn *sql.Conn) error {
	var ok bool

	err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", PostgresLockKey).Scan(&ok)
	if err != nil {
		return err //nolint:wrapcheck // Service.release logs it.
	}

	if !ok {
		return errLockNotHeld
	}

	return nil
}

// waits reports false: the lock is polled.
func (postgresLock) waits() bool { return false }

// postgresHeld is the query of whether a session holds the advisory lock
// on PostgresLockKey in the current database. pg_locks shows a bigint key
// with its high half in classid, its low half in objid, and objsubid 1.
const postgresHeld = "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory'" +
	" AND database = (SELECT oid FROM pg_database WHERE datname = current_database())" +
	" AND classid = ($1::bigint >> 32)::oid AND objid = ($1::bigint & 4294967295)::oid" +
	" AND objsubid = 1 AND granted)"

// held reads pg_locks.
func (postgresLock) held(ctx context.Context, db *sql.DB) (bool, error) {
	var ok bool

	err := db.QueryRowContext(ctx, postgresHeld, PostgresLockKey).Scan(&ok)

	return ok, err //nolint:wrapcheck // Service.CheckReady wraps it.
}

// mysqlLockName is the SQL of the user-level lock's name.
const mysqlLockName = "CONCAT(?, MD5(DATABASE()))"

// mysqlLock is MySQL's user-level lock named by mysqlLockName. Closing the
// connection releases it.
type mysqlLock struct{}

// try runs GET_LOCK, which returns 1 when it took the lock, 0 when wait
// seconds passed first, and NULL on an error.
func (mysqlLock) try(ctx context.Context, conn *sql.Conn, wait int) (bool, error) {
	var got sql.NullInt64

	err := conn.QueryRowContext(ctx, "SELECT GET_LOCK("+mysqlLockName+", ?)", MySQLLockPrefix, wait).
		Scan(&got)
	if err != nil {
		return false, err //nolint:wrapcheck // Service.wait wraps it.
	}

	if !got.Valid {
		return false, errLockNull
	}

	return got.Int64 == 1, nil
}

// release runs RELEASE_LOCK, which returns 1 when it released the lock, 0
// for a lock another session holds and NULL for one nobody holds.
func (mysqlLock) release(ctx context.Context, conn *sql.Conn) error {
	var got sql.NullInt64

	err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK("+mysqlLockName+")", MySQLLockPrefix).
		Scan(&got)
	if err != nil {
		return err //nolint:wrapcheck // Service.release logs it.
	}

	if got.Int64 != 1 {
		return errLockNotHeld
	}

	return nil
}

// waits reports true: GET_LOCK waits.
func (mysqlLock) waits() bool { return true }

// held runs IS_USED_LOCK, which returns the connection ID of the session
// that holds the lock, or NULL when none does.
func (mysqlLock) held(ctx context.Context, db *sql.DB) (bool, error) {
	var id sql.NullInt64

	err := db.QueryRowContext(ctx, "SELECT IS_USED_LOCK("+mysqlLockName+")", MySQLLockPrefix).
		Scan(&id)

	return id.Valid, err //nolint:wrapcheck // Service.CheckReady wraps it.
}

// session is a call's work on the one connection it runs everything on,
// which holds the migration lock where the dialect has one.
type session struct {
	// conn runs everything the call does.
	conn *sql.Conn
	// dialect is the database's SQL.
	dialect *dialect
	// held reports that conn holds the migration lock.
	held bool
	// broken reports that the connection's state is unknown: nothing
	// more is run on it.
	broken bool
}

// locked runs fn on a connection that holds the migration lock, where the
// dialect has one, as described in the package documentation. The lock
// is released, and the connection discarded, however fn ends, a panic
// included.
func (s *Service) locked(ctx context.Context, st *state, fn func(*session) error) (err error) {
	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	conn, err := st.db.Conn(lockCtx)
	if err != nil {
		return fmt.Errorf("%s: %w", ServiceName, s.waitError(ctx, lockCtx, stepLock, err))
	}

	sess := &session{conn: conn, dialect: st.dialect}

	defer func() { err = errors.Join(err, s.release(ctx, sess)) }()

	if sess.dialect.lock != nil {
		lock := sess.dialect.lock

		try := func(ctx context.Context, wait int) (bool, error) {
			return lock.try(ctx, conn, wait)
		}

		err = s.wait(ctx, lockCtx, stepLock, lock.waits(), try)
		if err != nil {
			sess.broken = true

			return fmt.Errorf("%s: %w", ServiceName, err)
		}

		sess.held = true
	}

	return fn(sess)
}

// The steps that wait for a lock, as their errors name them.
const (
	// stepLock takes the migration lock, or SQLite's write lock.
	stepLock = "acquire lock"
	// stepCommit commits a SQLite transaction, which waits for readers.
	stepCommit = "commit"
)

// wait calls try until it reports true, as described in the package
// documentation: within lockCtx, every lockPollInterval or, when the
// database waits itself, a second at a time. Waiting for stepLock is
// logged once. A failure is returned as waitError makes it for step.
func (s *Service) wait(
	ctx, lockCtx context.Context,
	step string,
	dbWaits bool,
	try func(ctx context.Context, wait int) (bool, error),
) error {
	wait := 0

	for attempt := 0; ; attempt++ {
		ok, err := try(lockCtx, wait)
		if err != nil {
			return s.waitError(ctx, lockCtx, step, err)
		}

		if ok {
			return nil
		}

		if attempt == 0 && step == stepLock {
			s.logger.Info("waiting for the migration lock").
				Duration("timeout", s.lockTimeout).
				Send()
		}

		wait, err = pause(lockCtx, dbWaits)
		if err != nil {
			return s.waitError(ctx, lockCtx, step, err)
		}
	}
}

// pause waits before the next attempt within lockCtx, and returns how
// many seconds that attempt may wait in the database: a second when it
// can wait itself and a second is left, with no pause here, and otherwise
// none, after lockPollInterval or what is left of lockCtx. It returns
// lockCtx's error when lockCtx ends.
func pause(lockCtx context.Context, dbWaits bool) (int, error) {
	deadline, _ := lockCtx.Deadline()

	remaining := time.Until(deadline)
	if remaining <= 0 {
		// The deadline passed by the clock; the context reports it once
		// its timer fires, which tells whose deadline it was.
		<-lockCtx.Done()

		return 0, lockCtx.Err() //nolint:wrapcheck // Service.waitError wraps it.
	}

	if dbWaits && remaining >= time.Second {
		return 1, nil
	}

	timer := time.NewTimer(min(lockPollInterval, remaining))
	defer timer.Stop()

	select {
	case <-lockCtx.Done():
		return 0, lockCtx.Err() //nolint:wrapcheck // Service.waitError wraps it.
	case <-timer.C:
		return 0, nil
	}
}

// waitError returns the error of the wait of step that ended with err, as
// the step and the cause: the caller's context error when ctx ended,
// ErrLockTimeout when only lockCtx did, and the driver's error otherwise.
func (s *Service) waitError(ctx, lockCtx context.Context, step string, err error) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("%s: %w", step, ctx.Err())
	case lockCtx.Err() != nil, errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", step, ErrLockTimeout)
	default:
		return fmt.Errorf("%s: %w", step, s.safeErr(err))
	}
}

// release releases the lock after a call, when the connection holds it,
// and discards the connection, or on SQLite returns it to the pool unless
// its state is unknown. A failure to release is logged: closing the
// connection releases the lock too, so the call's result stands.
func (s *Service) release(ctx context.Context, sess *session) error {
	if sess.held && !sess.broken {
		relCtx, cancel := detached(ctx)
		defer cancel()

		err := sess.dialect.lock.release(relCtx, sess.conn)
		if err != nil {
			s.logger.Warn("migration lock release failed, closing its connection").Send()
		}
	}

	if sess.dialect.reuseConn && !sess.broken {
		err := sess.conn.Close()
		if err != nil && !errors.Is(err, sql.ErrConnDone) {
			return fmt.Errorf("%s: release connection: %w", ServiceName, s.safeErr(err))
		}

		return nil
	}

	return discard(sess.conn)
}

// discard closes conn instead of returning it to the pool, so that no
// lock, transaction or session setting a migration made outlives the
// call: database/sql closes a connection whose Raw function returns
// driver.ErrBadConn. A connection database/sql has already closed, after
// the driver reported it broken, is sql.ErrConnDone, the state wanted.
//
// pgx's stdlib connection that borrows a pgxpool's, as gas/database's
// ModePgx makes, releases it to the pool when database/sql closes it, and
// the pool keeps an idle connection. Its pgx connection is closed first,
// so the pool destroys it instead.
func discard(conn *sql.Conn) error {
	err := conn.Raw(func(dc any) error {
		if pc, ok := dc.(interface{ Conn() *pgx.Conn }); ok {
			ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
			defer cancel()

			// pgx closes the socket whatever Close returns, which is
			// the state wanted.
			_ = pc.Conn().Close(ctx)
		}

		return driver.ErrBadConn
	})
	if err != nil && !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("%s: release connection: %w", ServiceName, err)
	}

	return nil
}

// detached returns a context that keeps ctx's values but not its
// cancellation, bounded by releaseTimeout.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
}
