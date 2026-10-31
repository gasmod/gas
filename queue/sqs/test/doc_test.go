package sqs_test

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// The example in the package documentation's Usage section, on
// ElasticMQ, with a queue of the test's own.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	endpoint := elasticMQ.use(t)
	name, _ := createQueue(t, endpoint, "", nil)
	ctx := t.Context()
	logger := &gas.NopLogger{}

	cfg := sqs.DefaultConfig()

	cfg.Queue.Region = "eu-west-1"
	cfg.Queue.Endpoint = endpoint
	cfg.Queue.AccessKeyID, cfg.Queue.SecretAccessKey = testKeyID, testSecret

	q, err := sqs.New(sqs.WithConfig(cfg))(nil, logger)
	require.NoError(t, err)
	require.NoError(t, q.Init(ctx))

	defer func() { require.NoError(t, q.Close(ctx)) }()

	err = q.Enqueue(ctx, name, []byte(`{"id":42}`))
	require.NoError(t, err)

	jobs, err := q.Dequeue(ctx, name, 1, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.JSONEq(t, `{"id":42}`, string(jobs[0].Body))
}

// replying returns a store on a fake that answers every request with r,
// without retries.
func replying(t *testing.T, r reply) *sqs.Service {
	t.Helper()

	return newFake(
		t,
		func(call) reply { return r },
	).open(t, func(s *sqs.Settings) { s.MaxRetries = 0 })
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
// Not parallel: it sets AWS variables.
//
//nolint:funlen,contextcheck // one case per documented message, made with the test helpers.
func TestDoc_Errors(t *testing.T) {
	isolateAWS(t)

	const orders = "https://sqs.example/000000000000/orders"

	rule := func(edit func(*sqs.Settings)) error {
		cfg := config("http://127.0.0.1:1")
		edit(&cfg.Queue)

		return initErr(t, sqs.WithConfig(cfg))
	}
	initial := func() *sqs.Service { return open(t, config("http://127.0.0.1:1")) }
	closedStore := func() *sqs.Service {
		s := initial()
		require.NoError(t, s.Close(t.Context()))

		return s
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	hole := "http://" + blackhole(t)
	enqueue := func(s *sqs.Service) error { return s.Enqueue(t.Context(), orders, []byte("v")) }
	enqueueReply := func(r reply) error { return enqueue(replying(t, r)) }

	cases := map[string]func() error{
		"gas/queue/sqs: logger is nil": func() error {
			_, err := sqs.New()(nil, nil)

			return err
		},
		"gas/queue/sqs: init: config is nil": func() error {
			return initErr(t, sqs.WithConfig(nil))
		},
		"gas/queue/sqs: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/queue/sqs: init: invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h": func() error {
			return rule(func(s *sqs.Settings) { s.VisibilityTimeout = time.Millisecond })
		},
		"gas/queue/sqs: init: invalid config: Queue.Endpoint must be an http or https URL with a host " +
			"and no user information": func() error {
			return rule(func(s *sqs.Settings) { s.Endpoint = "localhost:4566" })
		},
		"gas/queue/sqs: init: invalid config: AWS configuration failed to load": func() error {
			t.Setenv("AWS_PROFILE", "missing")
			defer t.Setenv("AWS_PROFILE", "")

			return rule(func(*sqs.Settings) {})
		},
		"gas/queue/sqs: init: no region": func() error {
			return rule(func(s *sqs.Settings) { s.Region = "" })
		},
		"gas/queue/sqs: init: no credentials: failed": func() error {
			return rule(func(s *sqs.Settings) { s.AccessKeyID = "" })
		},
		"gas/queue/sqs: init: no credentials: timed out": func() error {
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hole+"/creds")
			defer t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

			return rule(
				func(s *sqs.Settings) { s.AccessKeyID, s.OpTimeout = "", 200*time.Millisecond },
			)
		},
		"gas/queue/sqs: init: no credentials: timed out: context deadline exceeded": func() error {
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hole+"/creds")
			defer t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

			cfg := config(hole)

			cfg.Queue.AccessKeyID = ""

			s, _ := build(t, sqs.WithConfig(cfg))

			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()

			return s.Init(ctx)
		},
		"gas/queue/sqs: init: client is nil": func() error {
			return initErr(t, sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(nil))
		},
		"gas/queue/sqs: init: service already initialized": func() error {
			return initial().Init(t.Context())
		},
		"gas/queue/sqs: init: context canceled": func() error {
			s, _ := build(t, sqs.WithConfig(config(hole)))

			return s.Init(canceled)
		},
		"gas/queue/sqs: ready: service not initialized": func() error {
			s, _ := build(t)

			return s.CheckReady(t.Context())
		},
		"gas/queue/sqs: enqueue: invalid queue: empty": func() error {
			return initial().Enqueue(t.Context(), "", []byte("v"))
		},
		`gas/queue/sqs: enqueue "orders": invalid payload: empty`: func() error {
			return initial().Enqueue(t.Context(), orders, nil)
		},
		`gas/queue/sqs: enqueue "orders": service not initialized`: func() error {
			s, _ := build(t)

			return enqueue(s)
		},
		`gas/queue/sqs: enqueue "orders": service closed`: func() error {
			return enqueue(closedStore())
		},
		`gas/queue/sqs: enqueue "orders": context canceled`: func() error {
			return initial().Enqueue(canceled, orders, []byte("v"))
		},
		`gas/queue/sqs: enqueue "orders": context deadline exceeded`: func() error {
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()

			return open(t, config(hole)).Enqueue(ctx, orders, []byte("v"))
		},
		`gas/queue/sqs: enqueue "orders": timed out`: func() error {
			cfg := config(hole)

			cfg.Queue.OpTimeout = 200 * time.Millisecond

			return enqueue(open(t, cfg))
		},
		`gas/queue/sqs: enqueue "orders": connection refused`: func() error {
			cfg := config("http://" + closedAddr(t))

			cfg.Queue.MaxRetries = 0

			return enqueue(open(t, cfg))
		},
		`gas/queue/sqs: enqueue "orders": unreachable`: func() error {
			cfg := config("http://gas-queue-test.invalid")

			cfg.Queue.MaxRetries = 0

			return enqueue(open(t, cfg))
		},
		`gas/queue/sqs: enqueue "orders": queue does not exist (QueueDoesNotExist)`: func() error {
			return enqueueReply(awsError(http.StatusBadRequest, "QueueDoesNotExist", "", "no"))
		},
		`gas/queue/sqs: enqueue "orders": access denied (AccessDenied)`: func() error {
			return enqueueReply(awsError(http.StatusForbidden, "AccessDenied", "", "no"))
		},
		`gas/queue/sqs: enqueue "orders": throttled (RequestThrottled)`: func() error {
			return enqueueReply(
				awsError(http.StatusBadRequest, "RequestThrottled", "", "slow down"),
			)
		},
		`gas/queue/sqs: enqueue "orders": failed (InvalidParameterValue)`: func() error {
			return enqueueReply(awsError(http.StatusBadRequest, "InvalidParameterValue", "", "bad"))
		},
		`gas/queue/sqs: enqueue "orders": failed`: func() error {
			return enqueueReply(reply{status: http.StatusBadRequest, body: "not json"})
		},
		`gas/queue/sqs: ack "orders": invalid receipt handle (ReceiptHandleIsInvalid)`: func() error {
			s := replying(t, awsError(http.StatusBadRequest, "ReceiptHandleIsInvalid", "", "bad"))

			return s.Ack(t.Context(), orders, gas.Job{ReceiptHandle: "r"})
		},
		`gas/queue/sqs: nack "orders": message not in flight (MessageNotInflight)`: func() error {
			s := replying(t, awsError(http.StatusBadRequest, "MessageNotInflight", "", "gone"))

			return s.Nack(t.Context(), orders, gas.Job{ReceiptHandle: "r"})
		},
		`gas/queue/sqs: dequeue "orders": service closed`: func() error {
			release := make(chan struct{})
			f, arrived := blockingReceive(t, release, reply{body: `{}`})
			s := f.open(t, nil)
			err, closeErr := closeDuring(t, s, release, arrived)
			require.NoError(t, closeErr)

			return err
		},
		`gas/queue/sqs: dequeue "orders": invalid reply`: func() error {
			s := replying(t, reply{body: `{"Messages":[{"Body":"a"}]}`})
			_, err := s.Dequeue(t.Context(), orders, 1, 0)

			return err
		},
		"gas/queue/sqs: close: context deadline exceeded": func() error {
			f, arrived := holding(t, "SendMessage")
			s := f.open(t, nil)
			sent := make(chan error, 1)

			go func() { sent <- enqueue(s) }()

			<-arrived

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			err := s.Close(ctx)

			require.ErrorIs(t, <-sent, queue.ErrClosed)

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	documented := docLines(t, "../doc.go", "gas/queue/sqs: ")
	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), documented)

	cfg := config(hole)

	cfg.Queue.OpTimeout = 0

	err := initErr(t, sqs.WithConfig(cfg))
	require.EqualError(t, err, "gas/queue/sqs: init: invalid config: validate config: "+
		docLines(t, "../doc.go", "Key: ")[0])
}
