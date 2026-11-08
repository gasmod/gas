// Package lockwait computes how long a statement waits for a table lock
// while the tracking table is upgraded, in the units each server's
// setting takes.
//
// # Usage
//
//	pg := lockwait.Postgres(10 * time.Minute)
//	my := lockwait.MySQL(1500 * time.Millisecond)
//
// pg is `5000ms`, for PostgreSQL's lock_timeout, and my is 2, for MySQL's
// lock_wait_timeout.
//
// # Bound
//
// The bound is the lock timeout given or [Max], whichever is shorter. It
// is rounded up to the setting's unit, whole milliseconds for
// [Postgres] and whole seconds for [MySQL], and is at least one unit:
// PostgreSQL reads a lock_timeout below 1ms as 0, which means no
// timeout.
package lockwait
