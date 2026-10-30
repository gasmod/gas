// Package apikeystore keeps the API key service's keys in a SQL database,
// over queries sqlc generates for each dialect. A key is stored by the
// hash of its value, never the value.
//
// # Usage
//
// The API key service builds a [Store] with [New], calls [Store.Init] from
// its own Init, and then reads and writes keys:
//
//	store, err := apikeystore.New()(db, logger, migrations)
//	if err != nil {
//		return err
//	}
//	if err := store.Init(svc); err != nil {
//		return err
//	}
//	info, err := store.GetKeyByHash(ctx, hash)
//	if errors.Is(err, apikeystore.ErrNotFound) {
//		// no such key
//	}
//	if err == nil && info.DeletedAt != nil {
//		// the key was soft-deleted
//	}
//
// [Store.WithTx] returns a copy of the Store whose queries run in a
// transaction the caller owns.
//
// # Dialects and migrations
//
// Init picks the dialect from the database provider's driver name, as
// sqlutil.DialectOf does, and registers the migrations that create and
// extend the `__gas_auth_api_keys` table for that dialect with the
// migration manager, owned by the service passed to it. The migration
// manager runs them later; Init touches no table. The `postgres`, `mysql`
// and `sqlite` subdirectories hold each dialect's migrations, its queries,
// and the code sqlc generates from them.
//
// The first migration made a second index on `key_hash`, next to the one
// its UNIQUE constraint has; the migration
// `20261030150000_drop_api_keys_key_hash_index` drops it, on a new
// database and on one that ran the first migration alike.
//
// On MySQL the DSN needs `parseTime=true`, since times are scanned into
// [time.Time]; gas/database turns it on itself. `multiStatements` is not
// needed: gas/migrate sends a migration file's statements one at a time.
// The MySQL `scopes` column has no default: MySQL refuses a literal
// default on a TEXT column, and every insert sets it.
//
// # Columns
//
// Scopes are trimmed, and the empty ones dropped, as they are stored and
// as they are read. Postgres keeps them in a TEXT[] column; MySQL and
// SQLite keep them as one TEXT, joined with commas, such as `read,write`,
// so on those a scope holding a comma comes back as two. Metadata is a
// JSON object; JSON `null` reads as an empty map, and anything else that
// is not an object makes the read fail with [ErrInvalidMetadata]. SQLite
// keeps times as text, to the second, in UTC; see package sqlutil. A
// stored time in any other form makes the read fail, naming the column.
// MySQL and SQLite keep times in whole seconds, and the auth stores
// truncate every time to the second before writing it, so a stored time
// is up to a second earlier than the one written and an expiry can come
// up to a second early, never late.
// Postgres keeps microseconds.
//
// ListKeysBySubject returns keys newest first, by creation time, and keys
// with the same stored creation time, as two keys made within one second
// on SQLite or MySQL can have, by ID, descending, so the order is the
// same on every read.
//
// # Deletion
//
// A soft delete sets `deleted_at` and keeps the row: GetKeyByHash still
// finds the key, with [KeyInfo].DeletedAt set, so the caller can tell a
// revoked key from an unknown one, and ListKeysBySubject lists it only
// when asked to include revoked keys. A hard delete removes the row.
// [Store.DeleteExpiredKeys] hard-deletes every key whose expiry is at or
// before a given time, so a key whose expiry equals that time is deleted,
// as the API key service treats it as expired. It deletes soft-deleted
// keys too, and leaves keys without an expiry.
//
// # Errors
//
// GetKeyByHash returns [ErrNotFound], unwrapped, when no key has the
// hash. Any other failure is an error that starts
// with `gas/auth/apikey-store:` and the operation, and wraps the driver's
// error, sqlutil.ErrInvalidTime, or [ErrInvalidMetadata]. A read error
// never quotes a stored value:
//
//	gas/auth/apikey-store: get key: expires_at: invalid stored time: want the layout 2006-01-02 15:04:05
//	gas/auth/apikey-store: list keys: invalid stored metadata: want a JSON object
//
// Init fails, with an error that starts with the service's name, when the
// database provider has no open database, wrapping [ErrNoDatabase], when
// the driver is not one sqlutil.DialectOf knows, wrapping
// sqlutil.ErrUnsupportedDriver, or when the migration manager refuses the
// migrations.
package apikeystore
