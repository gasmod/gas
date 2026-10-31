package valkey_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	vk "github.com/valkey-io/valkey-go"

	"github.com/gasmod/gas/cache/valkey"
)

// A server's error reply keeps its text only for the codes whose text
// holds nothing of the command; every other reply, ERR included, is cut
// to its code, and a reply whose first word is not a known code, as
// valkey-go leaves an ERR reply, to ERR.
// The fake server sends each reply as valkey-server 8 does, with the key
// where the server echoes the command's arguments.
func TestCommand_ReplyRedaction(t *testing.T) {
	t.Parallel()

	kept := []string{
		"WRONGTYPE Operation against a key holding the wrong kind of value",
		"OOM command not allowed when used memory > 'maxmemory'.",
		"READONLY You can't write against a read only replica.",
		"LOADING Valkey is loading the dataset in memory",
		"MASTERDOWN Link with MASTER is down and replica-serve-stale-data is set to 'no'.",
		"MISCONF Errors writing to the AOF file: No space left on device",
		"NOREPLICAS Not enough good replicas to write.",
	}

	for _, text := range kept {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			err := replyErr(t, "-"+text+"\r\n")
			require.ErrorIs(t, err, valkey.ErrCommand)
			require.EqualError(t, err, "gas/cache/valkey: get: "+text)

			var verr *vk.ValkeyError

			require.ErrorAs(t, err, &verr)
		})
	}

	cut := map[string]string{
		"-ERR unknown command 'GET', with args beginning with: '" + secretKey + "' \r\n":       "ERR",
		blobError("ERR unknown command 'GET', with args beginning with: '" + secretKey + "' "): "ERR",
		"-NOPERM User zqxuser has no permissions to run the 'get' command\r\n":                 "NOPERM",
		"-MOVED 3999 127.0.0.1:6381\r\n":                                                       "MOVED",
		"-ASK 3999 127.0.0.1:6381\r\n":                                                         "ASK",
		"-REDIRECT 127.0.0.1:6380\r\n":                                                         "REDIRECT",
		"-BUSY zqx module reply\r\n":                                                           "BUSY",
		"-NOSCRIPT No matching script.\r\n":                                                    "NOSCRIPT",
		"-" + secretKey + " not found\r\n":                                                     "ERR",
		"-DB index is out of range\r\n":                                                        "ERR",
		"-\r\n":                                                                                "ERR",
	}

	for reply, want := range cut {
		t.Run(want+" "+reply[:min(len(reply), 12)], func(t *testing.T) {
			t.Parallel()

			err := replyErr(t, reply)
			require.ErrorIs(t, err, valkey.ErrCommand)
			require.EqualError(t, err, "gas/cache/valkey: get: "+want)

			var verr *vk.ValkeyError

			assert.NotErrorAs(t, err, &verr, "the reply is in the chain")
			assertNoSecrets(t, err.Error())
		})
	}
}

// blobError returns text as a RESP3 blob error.
func blobError(text string) string {
	return "!" + strconv.Itoa(len(text)) + "\r\n" + text + "\r\n"
}

// replyErr returns the error of a Get on a store connected to a fake
// server that answers GET with reply.
func replyErr(t *testing.T, reply string) error {
	t.Helper()

	cfg := config(fakeServer(t, reply))

	cfg.Cache.OpTimeout = 2 * time.Second

	_, err := open(t, cfg).Get(t.Context(), secretKey)

	return err
}

// A command whose context's deadline is no later than OpTimeout, and
// which times out, returns the context's error, never ErrCommand, though
// the connection's deadline can pass a moment before the context's timer
// fires. Repeated, since a check of the context's error alone fails
// only now and then.
func TestCommand_CallerDeadline(t *testing.T) {
	t.Parallel()

	s := stalled(t, time.Minute)

	for range 20 {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)

		_, err := s.Get(ctx, "k")

		cancel()

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotErrorIs(t, err, valkey.ErrCommand)
		require.EqualError(t, err, "gas/cache/valkey: get: context deadline exceeded")
	}

	// A context whose timer fires late: the connection's deadline passes
	// while its error is still nil, which a check of the context's error
	// alone reports as the store's timeout.
	lag := lagging(t, 20*time.Millisecond, 500*time.Millisecond)

	_, err := s.Get(lag, "k")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, valkey.ErrCommand)
	require.EqualError(t, err, "gas/cache/valkey: get: context deadline exceeded")

	// With OpTimeout the earlier deadline, the timeout is the store's.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	short := stalled(t, 50*time.Millisecond)

	_, err = short.Get(ctx, "k")
	require.ErrorIs(t, err, valkey.ErrCommand)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/cache/valkey: get: timed out")
}

// laggingContext is a context whose deadline passes before it ends: its
// Deadline is set, but Done closes, and Err is set, only later.
type laggingContext struct {
	context.Context //nolint:containedctx // the context it ends with.

	// deadline is what Deadline returns.
	deadline time.Time
}

// Deadline returns the deadline, which passes before the context ends.
func (c laggingContext) Deadline() (time.Time, bool) { return c.deadline, true }

// lagging returns a context whose deadline is in after, and which ends,
// with context.DeadlineExceeded, only in end.
func lagging(t *testing.T, after, end time.Duration) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), end)
	t.Cleanup(cancel)

	return laggingContext{Context: ctx, deadline: time.Now().Add(after)}
}
