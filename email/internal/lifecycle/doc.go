// Package lifecycle holds the lifecycle every sender of the module
// shares: Init, the calls in flight, and a Close that waits for them.
//
// # Usage
//
// A sender keeps a [Lifecycle] of what its Init opens, such as a client
// and its settings, and brackets each call with [Lifecycle.Begin] and
// [Session.End]:
//
//	err := lc.Init(ctx, func() (*state, error) { return open(ctx) })
//
//	sess, err := lc.Begin(ctx)
//	if err != nil {
//		return opError(op, err)
//	}
//	defer sess.End()
//	use(sess.Value)
//
//	sess, err := lc.Close(ctx)
//
// # Rules
//
// Init and Close run one at a time. Init on an open lifecycle returns
// email.ErrAlreadyInitialized and changes nothing; with an ended
// context, the context's error; when open fails, its error, and the
// lifecycle stays as it was. Otherwise it publishes a new [Session] with
// what open returned, whole, so a call sees one Init's value.
//
// Begin returns email.ErrNotInitialized before the first successful
// Init, email.ErrClosed after Close until Init succeeds again, and
// while Close runs, and the context's error when it has ended; else the
// open Session, counted as a call in flight until End.
//
// Close on a lifecycle that is not open returns nil and no Session.
// Otherwise it refuses new calls at once and waits for the calls in
// flight to end. When its context ends first, it cancels the Session's
// context, which the calls run within, and returns the context's error;
// the lifecycle stays closed either way. It returns the Session it
// closed, so that the sender can free what the Session holds. Init never
// clears the closed flag, so a call that finds no Session after a Close
// gets email.ErrClosed, even while a restart runs.
//
// # Errors
//
// The errors are the sentinels and the context's errors, unwrapped; the
// sender adds its service name and the operation.
package lifecycle
