// Package sessionstore keeps the session service's sessions in a SQL
// database, over queries sqlc generates for each dialect.
//
// # Usage
//
// The session service builds a [Store] with [New], calls [Store.Init] from
// its own Init, and then reads and writes sessions by the hash of their
// ID:
//
//	store, err := sessionstore.New()(db, logger, migrations)
//	if err != nil {
//		return err
//	}
//	if err := store.Init(svc); err != nil {
//		return err
//	}
//	sess, err := store.GetSession(ctx, cryptoutil.SHA256Hex(id))
//	if errors.Is(err, sessionstore.ErrNotFound) {
//		// no such session
//	}
//
// # Sessions
//
// A [Session] is keyed by IDHash, the SHA-256 hex of the session's ID, in
// the `id_hash` column. The store never sees the ID itself, so reading
// the table does not reveal a cookie that would authenticate.
//
// [Store.InsertSession] stores a new session, and fails when its IDHash is
// taken. [Store.GetSession] reads one back, and
// [Store.ListSessionsBySubject] every session of a subject, expired ones
// included, newest first and by ID hash, descending, for a tie.
// [Store.ExtendSession] sets a session's expiry and last active time.
// [Store.DeleteSession] deletes one session,
// [Store.DeleteSessionOfSubject] one session only if it belongs to a
// given subject, [Store.DeleteOtherSessionsOfSubject] every session of a
// subject but one, and [Store.DeleteSessionsBySubject] every session of
// a subject; none fails when there is nothing to delete.
// [Store.DeleteExpiredSessions] deletes the sessions whose expiry is at
// or before one time or whose creation time is at or before another, the
// sessions that are expired at those times, and returns how many it
// deleted.
//
// [Store.RotateSession] gives a session a new ID hash, address,
// User-Agent, expiry and last active time with one UPDATE of its row,
// keeping its subject, metadata and creation time. When no session has
// the old hash it changes nothing and returns [ErrNotFound], so of two
// calls that rotate the same session, only one succeeds. Since the row
// stays, a concurrent [Store.DeleteSessionsBySubject] deletes the
// rotated session as well: on Postgres the delete waits for the row and
// checks the new version, which a delete and insert would have hidden
// from it.
//
// Metadata is kept as a JSON object; a nil map is stored as `{}` and
// stored JSON `null` reads back as an empty map. Like any JSON, numbers
// read back as float64.
//
// # Dialects and migrations
//
// Init picks the dialect from the database provider's driver name, as
// sqlutil.DialectOf does, and registers that dialect's migrations with the
// migration manager through RegisterFS, owned by the service passed to
// it. The migration manager runs them later; Init touches no table. The
// `postgres`, `mysql` and `sqlite` subdirectories hold each dialect's
// migrations, its queries, and the code sqlc generates from them.
//
// There are two migrations per dialect. `20250323001_create_sessions`
// creates the `__gas_auth_sessions` table. `20261030140100_hash_session_ids`
// deletes every row, since the rows were keyed by the raw ID and no
// lookup by hash can match them, and renames the `id` column to
// `id_hash`. Applying it signs every user out. On MySQL it also gives the
// `metadata` and `user_agent` columns the defaults they have on Postgres
// and SQLite, `{}` and the empty string, which needs MySQL 8.0.13 or
// later. Its down migration deletes every row again and undoes the rest.
//
// SQLite keeps times as text, to the second, in UTC, dropping the
// fraction; see package sqlutil. A stored time in any other form makes
// GetSession fail rather than read as a zero time. The MySQL store
// truncates every time to the second before writing it, as SQLite's
// format does, so on both a stored time is up to a second earlier than
// the one written, and an expiry can come up to a second early, never
// late. Postgres keeps microseconds.
//
// # Errors
//
// GetSession returns [ErrNotFound], unwrapped, when no session has the ID
// hash; RotateSession returns it, unwrapped, when the session to rotate
// is missing. Any other failure is an error that starts with
// `gas/auth/session-store:` and the operation, and wraps the driver's
// error. Metadata that cannot be encoded or decoded as JSON
// is an error that does not quote it:
//
//	gas/auth/session-store: get session: expires_at: invalid stored time: want the layout 2006-01-02 15:04:05
//	gas/auth/session-store: get session: metadata: not a JSON object
//	gas/auth/session-store: insert session: metadata: cannot be encoded as JSON
//
// Init fails, with an error that starts with the service's name, when the
// database provider has no open database, wrapping [ErrNoDatabase], when
// the driver is not one sqlutil.DialectOf knows, wrapping
// sqlutil.ErrUnsupportedDriver, or when the migration manager rejects the
// migrations.
package sessionstore
