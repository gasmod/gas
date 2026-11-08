// Package tokenstore keeps the token service's single-use tokens in a SQL
// database, over queries sqlc generates for each dialect. A token is
// stored by the hash of its value, never the value.
//
// # Usage
//
// The token service builds a [Store] with [New], calls [Store.Init] from
// its own Init, and then stores and consumes tokens:
//
//	store, err := tokenstore.New()(db, logger, migrations)
//	if err != nil {
//		return err
//	}
//	if err := store.Init(svc); err != nil {
//		return err
//	}
//	rec, err := store.ConsumeTokenByHash(ctx, hash)
//	if errors.Is(err, tokenstore.ErrNotFound) {
//		// no such token, or already consumed
//	}
//
// # Dialects and migrations
//
// Init picks the dialect from the database provider's driver name, as
// sqlutil.DialectOf does, and registers that dialect's migrations with
// the migration manager, owned by the service passed to it. The migration
// manager runs them later; Init touches no table. The `postgres`, `mysql`
// and `sqlite` subdirectories hold each dialect's migrations, its
// queries, and the code sqlc generates from them. There are two
// migrations:
//
//   - `20250323003` creates the `__gas_auth_tokens` table, with a unique
//     `token_hash` and an index on `subject` and `purpose`.
//   - `20261030120000` drops the index `idx___gas_auth_tokens_token_hash`,
//     which the first migration made on `token_hash` next to the index
//     its unique constraint already has. Lookups by hash use the unique
//     index. On Postgres and SQLite the drop does nothing when the index
//     is gone already; its down migration makes the index again.
//
// SQLite keeps times as text, to the second, in UTC, dropping the
// fraction; see package sqlutil. The MySQL store truncates every time to
// the second before writing it, as SQLite's format does, so on both a
// stored time is up to a second earlier than the one written, and an
// expiry can come up to a second early, never late. Postgres keeps
// microseconds.
//
// # Consuming
//
// [Store.ConsumeTokenByHash] deletes the token and returns what it held in
// one atomic step, so of two callers consuming the same token at once,
// one gets the record and the other [ErrNotFound]. Postgres and SQLite do
// it with one `DELETE ... RETURNING`; MySQL, which has none, selects and
// deletes in a transaction and treats a delete that removed no row as not
// found. On SQLite, a stored expiry that does not parse is an error, and
// the token is gone all the same.
//
// # Deleting expired tokens
//
// [Store.DeleteExpiredTokens] deletes the tokens whose expiry is at or
// before the time it is given, so a token whose expiry equals that time
// is deleted, as the token service treats it as expired.
//
// # Errors
//
// ConsumeTokenByHash returns [ErrNotFound], unwrapped, when there is no
// token to consume. Any other failure is an error that starts with
// `gas/auth/token-store:` and the operation, and wraps the driver's
// error:
//
//	gas/auth/token-store: consume token: expires_at: invalid stored time: want the layout 2006-01-02 15:04:05
//
// Init fails, with an error that starts with the service's name, when the
// database provider has no open database, wrapping [ErrNoDatabase], when
// the driver is not one sqlutil.DialectOf knows, wrapping
// sqlutil.ErrUnsupportedDriver, or when the migration manager does not
// take the migrations, wrapping its error after `register migrations:`.
package tokenstore
