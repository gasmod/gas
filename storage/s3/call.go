package s3

import (
	"context"
	"time"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// call calls fn within ctx, the session's OpTimeout and its context, and
// returns its result, or its error as the failure of op, as failure
// describes. A nil result with a nil error fails as an invalid reply.
func call[T any](
	ctx context.Context,
	sess *lifecycle.Session[*state],
	op string,
	fn func(context.Context) (*T, error),
) (*T, error) {
	opDeadline := time.Now().Add(sess.Value.opTimeout)

	opCtx, cancel := context.WithDeadline(ctx, opDeadline)
	defer cancel()

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	defer stop()

	out, err := fn(opCtx)
	if err == nil && out == nil {
		err = errNoOutput
	}

	if err != nil {
		return nil, failure(ctx, sess, op, opDeadline, err)
	}

	return out, nil
}

// run calls fn as call does, for a request whose only result is its
// error.
func run(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	op string,
	fn func(context.Context) error,
) error {
	_, err := call(ctx, sess, op, func(ctx context.Context) (*struct{}, error) {
		return &struct{}{}, fn(ctx)
	})

	return err
}

// failure returns the error err of a request of op whose operation
// deadline was opDeadline: the context's error when ctx ended,
// storage.ErrClosed when a Close canceled the session,
// context.DeadlineExceeded for a timeout while ctx's deadline was the one
// in force, and else the error requestError makes. A zero opDeadline is
// never the one in force.
func failure(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	op string,
	opDeadline time.Time,
	err error,
) error {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return opError(op, ctxErr)
	}

	if sess.Context().Err() != nil {
		return opError(op, storage.ErrClosed)
	}

	callerDeadline, ok := ctx.Deadline()
	if ok && !opDeadline.IsZero() && !callerDeadline.After(opDeadline) && timedOut(err) {
		return opError(op, context.DeadlineExceeded)
	}

	return requestError(op, err)
}
