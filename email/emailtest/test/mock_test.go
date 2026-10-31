package emailtest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/emailtest"
)

// errBounce is what a test's function returns.
var errBounce = errors.New("bounce")

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	m := &emailtest.MockEmail{
		SendFn: func(context.Context, *gas.Email) error { return errBounce },
	}
	err := m.Send(ctx, &gas.Email{To: []string{"alice@example.com"}})

	require.ErrorIs(t, err, errBounce)
	assert.Equal(t, 1, m.CallCount("Send"))
}

// With a field nil, every method returns nil; with it set, the method
// returns what it returns, given the same arguments.
func TestMockEmail_Fns(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	m := &emailtest.MockEmail{}

	require.NoError(t, m.Send(ctx, &gas.Email{}))
	require.NoError(t, m.SendFromTemplate(ctx, &gas.TemplatedEmail{}))
	require.NoError(t, m.CheckReady(ctx))

	msg := &gas.Email{Subject: "s"}
	tmsg := &gas.TemplatedEmail{SubjectTemplate: "t"}

	m.SendFn = func(_ context.Context, got *gas.Email) error {
		assert.Same(t, msg, got)

		return errBounce
	}
	m.SendFromTemplateFn = func(_ context.Context, got *gas.TemplatedEmail) error {
		assert.Same(t, tmsg, got)

		return errBounce
	}
	m.CheckReadyFn = func(context.Context) error { return errBounce }

	require.ErrorIs(t, m.Send(ctx, msg), errBounce)
	require.ErrorIs(t, m.SendFromTemplate(ctx, tmsg), errBounce)
	require.ErrorIs(t, m.CheckReady(ctx), errBounce)
}

// Each call is recorded with copies of its message, nil for nil, so a
// caller that reuses the message, or changes what Calls returned, changes
// no recorded call; Data is the same value. Reset forgets the calls.
func TestMockEmail_Calls(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	m := &emailtest.MockEmail{}
	data := map[string]string{"Name": "Al"}

	msg := &gas.Email{To: []string{"a@example.com"}, Headers: map[string]string{"X": "1"}}
	tmsg := &gas.TemplatedEmail{Data: data, Cc: []string{"c@example.com"}}

	require.NoError(t, m.Send(ctx, msg))
	require.NoError(t, m.SendFromTemplate(ctx, tmsg))
	require.NoError(t, m.Send(ctx, nil))
	require.NoError(t, m.SendFromTemplate(ctx, nil))
	require.NoError(t, m.CheckReady(ctx))

	msg.To[0] = "changed@example.com"
	msg.Headers["X"] = "2"
	tmsg.Cc[0] = "changed@example.com"

	calls := m.Calls()
	require.Len(t, calls, 5)

	sent, ok := calls[0].Args[0].(*gas.Email)
	require.True(t, ok)
	assert.Equal(t, "Send", calls[0].Method)
	assert.Equal(t, []string{"a@example.com"}, sent.To)
	assert.Equal(t, "1", sent.Headers["X"])

	sent.To[0] = "again@example.com"

	tsent, ok := calls[1].Args[0].(*gas.TemplatedEmail)
	require.True(t, ok)
	assert.Equal(t, []string{"c@example.com"}, tsent.Cc)
	assert.Equal(t, data, tsent.Data)

	assert.Equal(t, []any{(*gas.Email)(nil)}, calls[2].Args)
	assert.Equal(t, []any{(*gas.TemplatedEmail)(nil)}, calls[3].Args)
	assert.Equal(t, "CheckReady", calls[4].Method)
	assert.Empty(t, calls[4].Args)

	again, ok := m.Calls()[0].Args[0].(*gas.Email)
	require.True(t, ok)
	assert.Equal(t, []string{"a@example.com"}, again.To)

	assert.Equal(t, 2, m.CallCount("Send"))
	assert.Equal(t, 1, m.CallCount("CheckReady"))

	m.Reset()
	assert.Empty(t, m.Calls())
	assert.Zero(t, m.CallCount("Send"))
}

// The methods, Calls, CallCount and Reset may run at once, for -race.
func TestMockEmail_Concurrent(t *testing.T) {
	t.Parallel()

	m := &emailtest.MockEmail{}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			assert.NoError(
				t,
				m.Send(context.Background(), &gas.Email{To: []string{"a@example.com"}}),
			)
			assert.NoError(t, m.SendFromTemplate(context.Background(), &gas.TemplatedEmail{}))

			_ = m.Calls()
			_ = m.CallCount("Send")
		})
	}

	wg.Wait()
	assert.Equal(t, 8, m.CallCount("Send"))

	m.Reset()
}
