package sqs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// fmt prints the secret access key and the session token of a Settings
// or a Config as `[redacted]`, whatever the verb.
func TestSecret_Redacted(t *testing.T) {
	t.Parallel()

	cfg := sqs.DefaultConfig()

	cfg.Queue.SecretAccessKey = "zqxsecret"
	cfg.Queue.SessionToken = "zqxtoken"

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		text := fmt.Sprintf(verb, cfg) + fmt.Sprintf(verb, cfg.Queue) + fmt.Sprintf(verb, *cfg)
		assert.NotContains(t, text, "zqx", verb)
	}

	assert.Equal(t, "[redacted]", cfg.Queue.SecretAccessKey.String())
	assert.Equal(t, "zqxsecret", string(cfg.Queue.SecretAccessKey))
}

// encoding/json, a YAML encoder and log/slog write the secret access key
// and the session token as `[redacted]`, in a Config and on their own.
func TestSecret_Marshal(t *testing.T) {
	t.Parallel()

	cfg := sqs.DefaultConfig()

	cfg.Queue.SecretAccessKey = "zqxsecret"
	cfg.Queue.SessionToken = "zqxtoken"

	j, err := json.Marshal(cfg) //nolint:musttag // untagged: the field names an encoder writes.
	require.NoError(t, err)
	assert.Contains(t, string(j), `"SecretAccessKey":"[redacted]","SessionToken":"[redacted]"`)

	y, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(y), "secretaccesskey: '[redacted]'")
	assert.Contains(t, string(y), "sessiontoken: '[redacted]'")

	var logs bytes.Buffer

	slog.New(slog.NewJSONHandler(&logs, nil)).
		Info("cfg", "cfg", cfg, "key", cfg.Queue.SecretAccessKey)
	slog.New(slog.NewTextHandler(&logs, nil)).
		Info("cfg", "cfg", cfg, "key", cfg.Queue.SessionToken)
	assert.Contains(t, logs.String(), `"key":"[redacted]"`)
	assert.Contains(t, logs.String(), `key=[redacted]`)

	for _, text := range []string{string(j), string(y), logs.String()} {
		assert.NotContains(t, text, "zqx")
	}
}

// DefaultConfig has no region, endpoint or credentials, the default
// operation timeout and retries, and the queue's own visibility timeout.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &sqs.Config{Queue: sqs.Settings{
		OpTimeout:  10 * time.Second,
		MaxRetries: 2,
	}}, sqs.DefaultConfig())
}

// Init binds the `queue` section from the ConfigProvider over
// DefaultConfig, the secret keys included.
func TestInit_Binds(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"queue": map[string]any{
			"region":            "eu-west-1",
			"endpoint":          f.srv.URL,
			"accesskeyid":       "AKID",
			"secretaccesskey":   "zqxsecret",
			"sessiontoken":      "zqxtoken",
			"optimeout":         "2s",
			"visibilitytimeout": "45s",
			"maxretries":        0,
		},
	})
	require.NoError(t, err)

	s, err := sqs.New()(p, nop)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	o := s.Client().Options()
	assert.Equal(t, "eu-west-1", o.Region)
	assert.Equal(t, f.srv.URL, aws.ToString(o.BaseEndpoint))

	creds, err := o.Credentials.Retrieve(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "AKID", creds.AccessKeyID)
	assert.Equal(t, "zqxsecret", creds.SecretAccessKey)
	assert.Equal(t, "zqxtoken", creds.SessionToken)
	assert.Equal(t, 1, o.Retryer.MaxAttempts())

	_, err = s.Dequeue(t.Context(), "orders", 1, 0)
	require.NoError(t, err)
	assert.InDelta(t, 45, f.received("ReceiveMessage")[0].body["VisibilityTimeout"], 0)
}

// A configuration given to WithConfig is copied on each Init and checked
// against its validate tags, and the rules run on it, naming the setting
// and never its value.
func TestInit_ConfigRules(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*sqs.Settings){
		"invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h": func(s *sqs.Settings) {
			s.VisibilityTimeout = 500 * time.Millisecond
		},
		"invalid config: Queue.Endpoint must be an http or https URL with a host and nothing more but /": func(
			s *sqs.Settings,
		) {
			s.Endpoint = "http://zqxuser:zqxpass@127.0.0.1:1"
		},
		"invalid config: validate config: Key: 'Config.Queue.OpTimeout' Error:Field validation for " +
			"'OpTimeout' failed on the 'gt' tag": func(s *sqs.Settings) { s.OpTimeout = 0 },
		"invalid config: validate config: Key: 'Config.Queue.VisibilityTimeout' Error:Field validation " +
			"for 'VisibilityTimeout' failed on the 'max' tag": func(s *sqs.Settings) { s.VisibilityTimeout = 13 * time.Hour },
		"invalid config: validate config: Key: 'Config.Queue.MaxRetries' Error:Field validation for " +
			"'MaxRetries' failed on the 'max' tag": func(s *sqs.Settings) { s.MaxRetries = sqs.MaxRetries + 1 },
	}

	for text, edit := range cases {
		cfg := config("http://127.0.0.1:1")
		edit(&cfg.Queue)

		err := initErr(t, sqs.WithConfig(cfg))
		require.ErrorIs(t, err, queue.ErrInvalidConfig, text)
		require.EqualError(t, err, "gas/queue/sqs: init: "+text)
		assertNoSecrets(t, err.Error())
	}

	for _, endpoint := range []string{"ftp://host", "host:4566", "http://", ":"} {
		cfg := config(endpoint)

		err := initErr(t, sqs.WithConfig(cfg))
		require.ErrorIs(t, err, queue.ErrInvalidConfig, endpoint)
	}

	cfg := config("https://sqs.example")

	cfg.Queue.VisibilityTimeout = 1500 * time.Millisecond

	s := open(t, cfg)
	require.NoError(t, s.CheckReady(t.Context()))
	assert.Equal(
		t,
		1500*time.Millisecond,
		cfg.Queue.VisibilityTimeout,
		"Init changed the caller's Config",
	)

	cfg.Queue.OpTimeout = 0

	require.NoError(t, s.Close(t.Context()))
	require.ErrorIs(
		t,
		s.Init(t.Context()),
		queue.ErrInvalidConfig,
		"Init copies the Config each time",
	)
}

// A VisibilityTimeout setting is sent rounded up to a whole second, and 0
// sends none, which is the queue's own.
func TestDequeue_Visibility(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)

	for _, c := range []struct {
		want    any
		setting time.Duration
	}{{nil, 0}, {2.0, 1500 * time.Millisecond}, {43200.0, queue.MaxVisibilityTimeout}} {
		s := f.open(t, func(set *sqs.Settings) { set.VisibilityTimeout = c.setting })

		_, err := s.Dequeue(t.Context(), "https://sqs.example/1/orders", 1, 0)
		require.NoError(t, err)

		calls := f.received("ReceiveMessage")
		assert.Equal(t, c.want, calls[len(calls)-1].body["VisibilityTimeout"], c.setting)
	}
}

// A Dequeue sends its wait in whole seconds, rounded up, and a wait of 0
// as an explicit 0, which the AWS SDK leaves out by itself; it asks for
// every attribute and the system attributes of the package
// documentation; and on a FIFO queue it sends an attempt ID of its own.
func TestDequeue_Request(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	for _, c := range []struct {
		wait time.Duration
		want float64
	}{{0, 0}, {time.Nanosecond, 1}, {500 * time.Millisecond, 1}, {time.Second, 1}, {1001 * time.Millisecond, 2}} {
		_, err := s.Dequeue(t.Context(), "orders", 3, c.wait)
		require.NoError(t, err)

		calls := f.received("ReceiveMessage")
		body := calls[len(calls)-1].body
		assert.InDelta(t, c.want, body["WaitTimeSeconds"], 0, c.wait)
		assert.InDelta(t, 3, body["MaxNumberOfMessages"], 0)
		assert.Equal(t, []any{"All"}, body["MessageAttributeNames"])
		assert.Equal(t, []any{
			"ApproximateReceiveCount", "ApproximateFirstReceiveTimestamp", "SentTimestamp",
			"MessageGroupId", "MessageDeduplicationId", "SequenceNumber",
		}, body["MessageSystemAttributeNames"])
		assert.NotContains(t, body, "ReceiveRequestAttemptId")
		assert.Equal(t, "https://sqs.example/000000000000/orders", body["QueueUrl"])
	}

	_, err := s.Dequeue(t.Context(), "orders.fifo", 1, 0)
	require.NoError(t, err)

	_, err = s.Dequeue(t.Context(), "orders.fifo", 1, 0)
	require.NoError(t, err)

	calls := f.received("ReceiveMessage")
	first := calls[len(calls)-2].body["ReceiveRequestAttemptId"]
	assert.NotEmpty(t, first)
	assert.NotEqual(t, first, calls[len(calls)-1].body["ReceiveRequestAttemptId"])
}

// Enqueue sends the payload, the delay in whole seconds rounded up, the
// group and dedupe IDs, and each attribute as a String.
func TestEnqueue_Request(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	require.NoError(t, s.Enqueue(t.Context(), "orders", []byte("hello"),
		gas.WithDelay(1500*time.Millisecond),
		gas.WithGroupID("tenant"),
		gas.WithJobAttributes(map[string]string{"k": "v"})))
	require.NoError(t, s.Enqueue(t.Context(), "orders.fifo", []byte("hi"),
		gas.WithGroupID("g"), gas.WithDedupeID("d")))
	require.NoError(t, s.Enqueue(t.Context(), "orders", []byte("plain")))

	calls := f.received("SendMessage")
	require.Len(t, calls, 3)
	assert.Equal(t, map[string]any{
		"QueueUrl":       "https://sqs.example/000000000000/orders",
		"MessageBody":    "hello",
		"DelaySeconds":   2.0,
		"MessageGroupId": "tenant",
		"MessageAttributes": map[string]any{
			"k": map[string]any{"DataType": "String", "StringValue": "v"},
		},
	}, calls[0].body)
	assert.Equal(t, map[string]any{
		"QueueUrl":               "https://sqs.example/000000000000/orders.fifo",
		"MessageBody":            "hi",
		"MessageGroupId":         "g",
		"MessageDeduplicationId": "d",
	}, calls[1].body)
	assert.Equal(t, map[string]any{
		"QueueUrl":    "https://sqs.example/000000000000/orders",
		"MessageBody": "plain",
	}, calls[2].body)
}

// Ack sends DeleteMessage, Nack ChangeMessageVisibility with an explicit
// 0, and NackAfter and Extend their duration in whole seconds, rounded
// up.
func TestAckNack_Request(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)
	ctx := t.Context()
	job := gas.Job{ReceiptHandle: "r1"}
	u := "https://sqs.example/1/orders"

	require.NoError(t, s.Ack(ctx, u, job))
	require.NoError(t, s.Nack(ctx, u, job))
	require.NoError(t, s.NackAfter(ctx, u, job, 1500*time.Millisecond))
	require.NoError(t, s.Extend(ctx, u, job, queue.MaxVisibilityTimeout))

	assert.Equal(t, []call{
		{op: "DeleteMessage", body: map[string]any{"QueueUrl": u, "ReceiptHandle": "r1"}},
		{
			op:   "ChangeMessageVisibility",
			body: map[string]any{"QueueUrl": u, "ReceiptHandle": "r1", "VisibilityTimeout": 0.0},
		},
		{
			op:   "ChangeMessageVisibility",
			body: map[string]any{"QueueUrl": u, "ReceiptHandle": "r1", "VisibilityTimeout": 2.0},
		},
		{
			op: "ChangeMessageVisibility",
			body: map[string]any{
				"QueueUrl":          u,
				"ReceiptHandle":     "r1",
				"VisibilityTimeout": 43200.0,
			},
		},
	}, f.received(""))
}

// Dequeue maps each message to a job: the user attributes with a string
// value, Number ones included and Binary ones left out, then the system
// attributes it asked for, which take the place of a user attribute of
// the same name; any other system attribute is left out.
func TestDequeue_Jobs(t *testing.T) {
	t.Parallel()

	f := newFake(t, func(c call) reply {
		if c.op != "ReceiveMessage" {
			return ok(c)
		}

		return reply{body: `{"Messages":[{
			"MessageId":"m1","ReceiptHandle":"r1","Body":"hello",
			"Attributes":{"SentTimestamp":"1000","ApproximateReceiveCount":"3","SenderId":"AIDAZQX"},
			"MessageAttributes":{
				"kind":{"DataType":"String","StringValue":"created"},
				"n":{"DataType":"Number","StringValue":"42"},
				"bin":{"DataType":"Binary","BinaryValue":"AAE="},
				"SentTimestamp":{"DataType":"String","StringValue":"9999"},
				"ApproximateFirstReceiveTimestamp":{"DataType":"String","StringValue":"spoof"}
			}}]}`}
	})
	s := f.open(t, nil)

	jobs, err := s.Dequeue(t.Context(), "orders", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, []gas.Job{{
		ID:            "m1",
		ReceiptHandle: "r1",
		Body:          []byte("hello"),
		Attributes: map[string]string{
			"kind":                    "created",
			"n":                       "42",
			"SentTimestamp":           "1000",
			"ApproximateReceiveCount": "3",
		},
	}}, jobs)
}

// A queue name is resolved with GetQueueUrl once, and the URL kept, until
// a request finds the queue does not exist, which drops it; a queue URL
// is used as it is, and never resolved.
func TestQueueURL_Cache(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)
	ctx := t.Context()

	require.NoError(t, s.Enqueue(ctx, "orders", []byte("1")))
	require.NoError(t, s.Enqueue(ctx, "orders", []byte("2")))
	require.NoError(t, s.Enqueue(ctx, "https://sqs.example/1/other", []byte("3")))
	assert.Len(t, f.received("GetQueueUrl"), 1)

	f.setHandle(func(c call) reply {
		if c.op == "SendMessage" {
			return awsError(
				http.StatusBadRequest,
				"QueueDoesNotExist",
				"AWS.SimpleQueueService.NonExistentQueue",
				"The specified queue does not exist.",
			)
		}

		return ok(c)
	})

	err := s.Enqueue(ctx, "orders", []byte("4"))
	require.ErrorIs(t, err, queue.ErrQueueNotFound)
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.EqualError(
		t,
		err,
		`gas/queue/sqs: enqueue "orders": queue does not exist (AWS.SimpleQueueService.NonExistentQueue)`,
	)

	f.setHandle(nil)
	require.NoError(t, s.Enqueue(ctx, "orders", []byte("5")))
	assert.Len(t, f.received("GetQueueUrl"), 2)

	f.setHandle(func(c call) reply {
		if c.op == "GetQueueUrl" {
			return reply{body: `{}`}
		}

		return ok(c)
	})

	err = s.Enqueue(ctx, "fresh", []byte("6"))
	require.ErrorIs(t, err, sqs.ErrCommand)
	require.EqualError(t, err, `gas/queue/sqs: enqueue "fresh": invalid reply`)
}

// An error reply is classified by its code: the reason, the code when it
// is a known one, and the sentinels. The reply's message, which can quote
// a receipt handle or a parameter, is never kept, nor an unknown code.
//
//nolint:funlen // one case per reason.
func TestErrors_Replies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		reply     reply
		text      string
		sentinels []error
	}{
		{
			awsError(400, "QueueDoesNotExist", "", "zqx"),
			"queue does not exist (QueueDoesNotExist)",
			[]error{queue.ErrQueueNotFound, sqs.ErrCommand},
		},
		{
			awsError(403, "AccessDenied", "", "User zqx is not authorized"),
			"access denied (AccessDenied)",
			[]error{sqs.ErrAccessDenied, sqs.ErrCommand},
		},
		{
			awsError(400, "InvalidClientTokenId", "", "zqx"),
			"access denied (InvalidClientTokenId)",
			[]error{sqs.ErrAccessDenied, sqs.ErrCommand},
		},
		{
			awsError(400, "ThrottlingException", "RequestThrottled", "zqx"),
			"throttled (RequestThrottled)",
			[]error{sqs.ErrThrottled, sqs.ErrCommand},
		},
		{
			awsError(
				400,
				"ReceiptHandleIsInvalid",
				"ReceiptHandleIsInvalid",
				"The receipt handle zqx is not valid.",
			),
			"invalid receipt handle (ReceiptHandleIsInvalid)",
			[]error{queue.ErrInvalidReceipt, sqs.ErrCommand},
		},
		{
			awsError(400, "MessageNotInflight", "AWS.SimpleQueueService.MessageNotInflight", "zqx"),
			"message not in flight (AWS.SimpleQueueService.MessageNotInflight)",
			[]error{queue.ErrNotInFlight, sqs.ErrCommand},
		},
		{
			awsError(
				400,
				"InvalidParameterValue",
				"",
				"Value zqx for parameter ReceiptHandle is invalid.",
			),
			"failed (InvalidParameterValue)",
			[]error{sqs.ErrCommand},
		},
		{
			awsError(400, "zqx<script>", "", "zqx"),
			"failed",
			[]error{sqs.ErrCommand},
		},
		{
			reply{status: 400, body: "zqx not json"},
			"failed",
			[]error{sqs.ErrCommand},
		},
	}

	for _, c := range cases {
		f := newFake(t, func(call) reply { return c.reply })
		s := f.open(t, func(set *sqs.Settings) { set.MaxRetries = 0 })

		err := s.Ack(t.Context(), "https://sqs.example/1/orders", gas.Job{ReceiptHandle: "zqx"})
		for _, sentinel := range c.sentinels {
			require.ErrorIs(t, err, sentinel, c.text)
		}

		require.EqualError(t, err, `gas/queue/sqs: ack "orders": `+c.text)
		assertNoSecrets(t, err.Error(), "sqs.example")
	}
}

// MaxRetries is how many times a failed request is retried: a request
// that keeps failing is sent MaxRetries+1 times.
func TestMaxRetries(t *testing.T) {
	t.Parallel()

	for _, retries := range []int{0, 2} {
		f := newFake(t, func(call) reply { return awsError(500, "InternalError", "", "boom") })
		s := f.open(t, func(set *sqs.Settings) {
			// The SDK's backoff between attempts can reach several
			// seconds, which the default OpTimeout would cut.
			set.MaxRetries, set.OpTimeout = retries, time.Minute
		})

		err := s.Enqueue(t.Context(), "https://sqs.example/1/orders", []byte("v"))
		require.ErrorIs(t, err, sqs.ErrCommand)
		require.EqualError(t, err, `gas/queue/sqs: enqueue "orders": failed (InternalError)`)
		assert.Len(t, f.received("SendMessage"), retries+1)
	}
}

// AWS_MAX_ATTEMPTS and AWS_RETRY_MODE do not override MaxRetries: the
// client once applied AWS_MAX_ATTEMPTS over the store's retryer, so a
// MaxRetries of 0 still retried.
//
// Not parallel: it sets environment variables.
func TestMaxRetries_EnvIgnored(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_MAX_ATTEMPTS", "5")
	t.Setenv("AWS_RETRY_MODE", "adaptive")

	for _, retries := range []int{0, 1} {
		f := newFake(t, func(call) reply { return awsError(500, "InternalError", "", "boom") })
		s := f.open(t, func(set *sqs.Settings) {
			set.MaxRetries, set.OpTimeout = retries, time.Minute
		})

		err := s.Enqueue(t.Context(), "https://sqs.example/1/orders", []byte("v"))
		require.ErrorIs(t, err, sqs.ErrCommand)
		assert.Len(t, f.received("SendMessage"), retries+1)
	}
}

// An endpoint with a path, a query or a fragment is refused, so a token
// in it cannot reach the logs; a bare host, with or without `/`, is
// accepted.
func TestInit_EndpointRule(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"https://sqs.example/path",
		"https://sqs.example/?X-Token=zqx",
		"https://sqs.example?",
		"https://sqs.example#zqx",
		"https:sqs.example",
	} {
		err := initErr(t, sqs.WithConfig(config(endpoint)))
		require.ErrorIs(t, err, queue.ErrInvalidConfig, endpoint)
		assert.NotContains(t, err.Error(), "zqx")
	}

	for _, endpoint := range []string{"https://sqs.example", "https://sqs.example/"} {
		s, _ := build(t, sqs.WithConfig(config(endpoint)))
		require.NoError(t, s.Init(t.Context()), endpoint)
		require.NoError(t, s.Close(t.Context()))
	}
}

// Before Init, Client is nil; after it, the *sqs.Client the store built,
// or nil for an API given to WithClient that is not one; after Close,
// nil.
func TestClient(t *testing.T) {
	t.Parallel()

	s, _ := build(t, sqs.WithConfig(config("https://sqs.example")))
	assert.Nil(t, s.Client())

	require.NoError(t, s.Init(t.Context()))
	assert.NotNil(t, s.Client())

	require.NoError(t, s.Close(t.Context()))
	assert.Nil(t, s.Client())

	raw := rawClient("https://sqs.example")
	given := open(t, sqs.DefaultConfig(), sqs.WithClient(raw))
	assert.Same(t, raw, given.Client())

	fake := open(t, sqs.DefaultConfig(), sqs.WithClient(&stubAPI{}))
	assert.Nil(t, fake.Client())
}

// stubAPI is an sqs.API that answers every request with no error and
// nothing, for the tests that need an API that is not an *sqs.Client.
type stubAPI struct{ sqs.API }

// CheckReady reports the store's state without I/O.
func TestCheckReady(t *testing.T) {
	t.Parallel()

	s, _ := build(t, sqs.WithConfig(config("http://127.0.0.1:1")))

	err := s.CheckReady(t.Context())
	require.ErrorIs(t, err, queue.ErrNotInitialized)
	require.EqualError(t, err, "gas/queue/sqs: ready: service not initialized")

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.CheckReady(t.Context()))

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	err = s.CheckReady(canceled)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/queue/sqs: ready: context canceled")

	require.NoError(t, s.Close(t.Context()))

	err = s.CheckReady(t.Context())
	require.ErrorIs(t, err, queue.ErrClosed)
	require.EqualError(t, err, "gas/queue/sqs: ready: service closed")
}

// Init logs `service initialized` with the region, whether an endpoint
// and a client were given, and the operation timeout; Close logs
// `service closed`. Neither holds a credential or the endpoint.
func TestLogs(t *testing.T) {
	t.Parallel()

	s, log := build(t, sqs.WithConfig(config("http://127.0.0.1:1")))
	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Close(t.Context()))

	entries := log.entries()
	require.Len(t, entries, 2)
	assert.Equal(t, entry{level: "info", msg: "service initialized", fields: map[string]any{
		"service":     sqs.ServiceName,
		"region":      testRegion,
		"endpoint":    true,
		"client":      false,
		"credentials": "static",
		"op_timeout":  sqs.DefaultOpTimeout,
	}}, entries[0])
	assert.Equal(t, entry{level: "info", msg: "service closed", fields: map[string]any{
		"service": sqs.ServiceName,
	}}, entries[1])
	assertNoSecrets(t, log.text())
}

// Init on a store given an API logs no region.
func TestLogs_Client(t *testing.T) {
	t.Parallel()

	s, log := build(t, sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(&stubAPI{}))
	require.NoError(t, s.Init(t.Context()))
	assert.Empty(t, log.entries()[0].fields["region"])
	assert.Equal(t, true, log.entries()[0].fields["client"])
	assert.NotContains(t, log.entries()[0].fields, "credentials")
}

// Calls may run while the store restarts: each sees one Init's client
// throughout, or ErrClosed, never ErrNotInitialized, which -race checks.
func TestRestart_Concurrent(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				err := s.Enqueue(context.Background(), "orders", []byte("v"))
				if err != nil && !errors.Is(err, queue.ErrClosed) {
					t.Errorf("enqueue: %v", err)
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, s.Close(t.Context()))
		require.NoError(t, s.Init(t.Context()))
	}

	close(stop)
	wg.Wait()
}

// A store built with WithClient uses the client as it is and needs no
// region or credentials. A nil client, or a nil *sqs.Client, fails Init.
func TestWithClient(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := open(t, sqs.DefaultConfig(), sqs.WithClient(rawClient(f.srv.URL)))

	require.NoError(t, s.Enqueue(t.Context(), "orders", []byte("v")))
	assert.Len(t, f.received("SendMessage"), 1)

	err := initErr(t, sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(nil))
	require.ErrorIs(t, err, sqs.ErrNilClient)
	require.EqualError(t, err, "gas/queue/sqs: init: client is nil")

	var typedNil *awssqs.Client

	err = initErr(t, sqs.WithConfig(sqs.DefaultConfig()), sqs.WithClient(typedNil))
	require.ErrorIs(t, err, sqs.ErrNilClient)
}

// Every message system attribute the AWS SDK knows is a reserved name, so
// no user attribute can take one.
func TestReservedAttributes_SDK(t *testing.T) {
	t.Parallel()

	for _, name := range types.MessageSystemAttributeName("").Values() {
		assert.True(t, queue.IsReservedAttribute(string(name)), name)
	}

	assert.True(t, queue.IsReservedAttribute(string(types.MessageSystemAttributeNameAll)))
}

// Name is ServiceName.
func TestName(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, "gas/queue/sqs", s.Name())
}
