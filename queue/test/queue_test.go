package queue_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/memory"
)

// ParseQueue reads a queue name or an http or https URL ending with one,
// tells FIFO queues by the `.fifo` suffix, and refuses anything else
// without quoting it.
func TestParseQueue(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", queue.MaxQueueNameLength)
	valid := map[string]queue.Queue{
		"orders":           {ID: "orders", Name: "orders"},
		"Orders_1-a":       {ID: "Orders_1-a", Name: "Orders_1-a"},
		"orders.fifo":      {ID: "orders.fifo", Name: "orders.fifo", FIFO: true},
		long:               {ID: long, Name: long},
		long[5:] + ".fifo": {ID: long[5:] + ".fifo", Name: long[5:] + ".fifo", FIFO: true},
		"https://sqs.eu-west-1.amazonaws.com/123456789012/orders.fifo": {
			ID:   "https://sqs.eu-west-1.amazonaws.com/123456789012/orders.fifo",
			Name: "orders.fifo", FIFO: true, URL: true,
		},
		"http://localhost:9324/000000000000/orders": {
			ID: "http://localhost:9324/000000000000/orders", Name: "orders", URL: true,
		},
	}

	for id, want := range valid {
		got, err := queue.ParseQueue(id)
		require.NoError(t, err, id)
		assert.Equal(t, want, got, id)
	}

	err := parseErr("")
	require.ErrorIs(t, err, queue.ErrInvalidQueue)
	require.EqualError(t, err, "invalid queue: empty")

	for _, id := range []string{
		" ", "a b", "orders.", ".fifo", "a.fifo.fifo", "ä", long + "a", long[4:] + ".fifo",
		"https://", "https:///orders", "http://user:pw@host/orders", "http://host/", "http://host/a b",
		"http://host/%zz", "ftp://host/orders", "https://host/1/orders?x=1", "https://host/1/orders#f",
		"https://host/1/orders/", "https://host",
	} {
		_, err := queue.ParseQueue(id)
		require.ErrorIs(t, err, queue.ErrInvalidQueue, id)
		require.EqualError(t, err, "invalid queue: not a queue name or URL", id)
	}
}

// Size counts the payload and each attribute's name, value and type.
func TestMessage_Size(t *testing.T) {
	t.Parallel()

	m := queue.NewMessage([]byte("12345"), []gas.EnqueueOption{
		gas.WithJobAttributes(map[string]string{"ab": "xyz", "c": "d"}),
		gas.WithGroupID("g"),
		gas.WithDedupeID("d"),
		gas.WithDelay(time.Second),
	})

	assert.Equal(t, 5+(2+6+3)+(1+6+1), m.Size())
	assert.Equal(t, "g", m.GroupID)
	assert.Equal(t, "d", m.DedupeID)
	assert.Equal(t, time.Second, m.Delay)
}

// CheckMessage reports the first rule a message breaks, the attributes
// first, then the payload, the delay, the group ID and the dedupe ID.
func TestCheckMessage_Order(t *testing.T) {
	t.Parallel()

	fifo, err := queue.ParseQueue("q.fifo")
	require.NoError(t, err)

	m := queue.Message{
		Attributes: map[string]string{"": "v"},
		DedupeID:   " ",
		Delay:      -1,
	}

	for _, text := range []string{
		"invalid attribute: a name is empty or longer than 256 bytes",
		"invalid payload: empty",
		"invalid argument: delay must be 0 to 15m0s",
		"invalid argument: a FIFO queue needs a group ID",
		"invalid argument: dedupe ID must be 1 to 128 ASCII letters, digits and punctuation",
	} {
		err := queue.CheckMessage(fifo, m)
		require.EqualError(t, err, text)

		switch {
		case m.Attributes != nil:
			m.Attributes = nil
		case m.Payload == nil:
			m.Payload = []byte("v")
		case m.Delay < 0:
			m.Delay = 0
		case m.GroupID == "":
			m.GroupID = "g"
		}
	}

	m.DedupeID = "d"
	require.NoError(t, queue.CheckMessage(fifo, m))
}

// The checks of Dequeue's arguments, of a visibility timeout and of a
// receipt handle accept their bounds and refuse what is past them.
func TestChecks_Bounds(t *testing.T) {
	t.Parallel()

	require.NoError(t, queue.CheckDequeue(1, 0))
	require.NoError(t, queue.CheckDequeue(queue.MaxMessages, queue.MaxWait))
	require.ErrorIs(t, queue.CheckDequeue(0, 0), queue.ErrInvalidArgument)
	require.ErrorIs(t, queue.CheckDequeue(1, -1), queue.ErrInvalidArgument)

	require.NoError(t, queue.CheckVisibilityTimeout(0))
	require.NoError(t, queue.CheckVisibilityTimeout(queue.MaxVisibilityTimeout))
	require.ErrorIs(t, queue.CheckVisibilityTimeout(-1), queue.ErrInvalidArgument)

	require.NoError(t, queue.CheckReceipt(gas.Job{ReceiptHandle: "r"}))
	require.ErrorIs(t, queue.CheckReceipt(gas.Job{}), queue.ErrInvalidReceipt)
}

// WholeSeconds rounds up, from 0 to the longest visibility timeout.
func TestWholeSeconds(t *testing.T) {
	t.Parallel()

	for d, want := range map[time.Duration]int32{
		0:                          0,
		time.Nanosecond:            1,
		time.Second:                1,
		time.Second + 1:            2,
		queue.MaxDelay:             900,
		queue.MaxVisibilityTimeout: 43200,
	} {
		assert.Equal(t, want, queue.WholeSeconds(d), d)
	}
}

// SystemAttributes lists the reported system attributes, each a reserved
// name, and the other reserved names are SQS's other system attributes.
func TestSystemAttributes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"ApproximateReceiveCount", "ApproximateFirstReceiveTimestamp", "SentTimestamp",
		"MessageGroupId", "MessageDeduplicationId", "SequenceNumber",
	}, queue.SystemAttributes())

	for _, name := range append(queue.SystemAttributes(),
		"All", "SenderId", "AWSTraceHeader", "SqsManagedSseEnabled", "DeadLetterQueueSourceArn") {
		assert.True(t, queue.IsReservedAttribute(name), name)
	}

	assert.False(t, queue.IsReservedAttribute("sentTimestamp"))
	assert.False(t, queue.IsReservedAttribute("kind"))
}

// errNotHandled is the error of a job the Usage example does not handle.
var errNotHandled = errors.New("not handled")

// The example in the package documentation's Usage section, on the
// memory store.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	q, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, q.Init(ctx))

	defer func() { require.NoError(t, q.Close(context.Background())) }()

	require.NoError(t, q.CreateQueue(ctx, "orders", memory.QueueConfig{}))
	require.NoError(t, q.Enqueue(ctx, "orders", []byte(`{"id":1}`)))

	err = q.Enqueue(ctx, "orders", []byte(`{"id":42}`),
		gas.WithJobAttributes(map[string]string{"kind": "created"}))
	require.NoError(t, err)

	handle := func(job gas.Job) error {
		if job.Attributes["kind"] == "created" {
			return nil
		}

		return errNotHandled
	}

	jobs, err := q.Dequeue(ctx, "orders", 10, 20*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	for _, job := range jobs {
		if handle(job) == nil {
			err = q.Ack(ctx, "orders", job)
		} else {
			err = q.Nack(ctx, "orders", job)
		}

		require.NoError(t, err)
	}

	left, err := q.Dequeue(ctx, "orders", 10, 0)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.JSONEq(t, `{"id":1}`, string(left[0].Body))
}

// The error messages the package documentation lists are the ones the
// checks return, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	std, err := queue.ParseQueue("q")
	require.NoError(t, err)

	fifo, err := queue.ParseQueue("q.fifo")
	require.NoError(t, err)

	msg := func(payload string, opts ...gas.EnqueueOption) queue.Message {
		return queue.NewMessage([]byte(payload), opts)
	}
	attrs := func(a map[string]string) queue.Message {
		return msg("v", gas.WithJobAttributes(a))
	}
	eleven := map[string]string{}

	for k := range strings.SplitSeq("a b c d e f g h i j k", " ") {
		eleven[k] = "v"
	}

	errs := []error{
		parseErr(""),
		parseErr("a b"),
		queue.CheckMessage(std, msg("")),
		queue.CheckMessage(std, msg("\xff")),
		queue.CheckMessage(std, msg("ab\x00")),
		queue.CheckMessage(
			std,
			msg(
				strings.Repeat("x", queue.MaxPayloadSize-7),
				gas.WithJobAttributes(map[string]string{"k": "v"}),
			),
		),
		queue.CheckMessage(std, attrs(eleven)),
		queue.CheckMessage(std, attrs(map[string]string{"": "v"})),
		queue.CheckMessage(std, attrs(map[string]string{"a/b": "v"})),
		queue.CheckMessage(std, attrs(map[string]string{"a..b": "v"})),
		queue.CheckMessage(std, attrs(map[string]string{"aws.x": "v"})),
		queue.CheckMessage(std, attrs(map[string]string{"SentTimestamp": "v"})),
		queue.CheckMessage(std, attrs(map[string]string{"k": ""})),
		queue.CheckMessage(std, attrs(map[string]string{"k": "\xff"})),
		queue.CheckMessage(std, attrs(map[string]string{"k": "\x01"})),
		queue.CheckMessage(std, msg("v", gas.WithDelay(time.Hour))),
		queue.CheckMessage(fifo, msg("v", gas.WithDelay(time.Second))),
		queue.CheckMessage(fifo, msg("v")),
		queue.CheckMessage(std, msg("v", gas.WithGroupID(" "))),
		queue.CheckMessage(std, msg("v", gas.WithDedupeID("d"))),
		queue.CheckMessage(fifo, msg("v", gas.WithGroupID("g"), gas.WithDedupeID(" "))),
		queue.CheckDequeue(0, 0),
		queue.CheckDequeue(1, time.Minute),
		queue.CheckVisibilityTimeout(13 * time.Hour),
		queue.CheckReceipt(gas.Job{}),
	}

	got := make([]string, 0, len(errs))

	for _, err := range errs {
		require.Error(t, err)

		got = append(got, err.Error())
	}

	assert.ElementsMatch(t, documented(t), got)
}

// parseErr returns the error of ParseQueue of id.
func parseErr(id string) error {
	_, err := queue.ParseQueue(id)

	return err
}

// documented returns the error lines of the package documentation: the
// tab-indented lines of its Errors section.
func documented(t *testing.T) []string {
	t.Helper()

	f, err := os.Open("../doc.go")
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var (
		out    []string
		inErrs bool
	)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "// # Errors" {
			inErrs = true
		}

		code, ok := strings.CutPrefix(line, "//\t")
		if inErrs && ok {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}

// errShape matches every error the checks make, none of which quotes the
// input but for a reserved attribute name.
var errShape = regexp.MustCompile(`^(invalid queue: (empty|not a queue name or URL)|` +
	`invalid payload: (empty|not valid UTF-8|a character SQS does not allow, at byte \d+|` +
	`\d+ bytes with the attributes, more than 1048576)|` +
	`invalid attribute: (\d+ attributes, at most 10|a name [^"]*|"[A-Za-z]+" is a system attribute|a value [^"]*)|` +
	`invalid argument: [^"]*)$`)

// FuzzParseQueue checks that ParseQueue never panics, is deterministic,
// parses only names and URLs whose name passes the rules, and fails only
// with the documented texts.
func FuzzParseQueue(f *testing.F) {
	for _, seed := range []string{"", "orders", "q.fifo", "https://h/1/q", "http://u:zqx@h/q", "a b", "\x00"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, id string) {
		q, err := queue.ParseQueue(id)
		q2, err2 := queue.ParseQueue(id)
		assert.Equal(t, q, q2)
		assert.Equal(t, err, err2)

		if err != nil {
			assert.Regexp(t, errShape, err.Error())

			return
		}

		assert.Equal(t, id, q.ID)
		assert.NotEmpty(t, q.Name)
		assert.LessOrEqual(t, len(q.Name), queue.MaxQueueNameLength)
		assert.Equal(t, strings.HasSuffix(q.Name, ".fifo"), q.FIFO)
	})
}

// FuzzCheckMessage checks that CheckMessage never panics, is
// deterministic, and fails only with the documented shapes, which never
// quote the payload, an attribute or an ID.
func FuzzCheckMessage(f *testing.F) {
	f.Add([]byte("hello"), "k", "v", "g", "d", int64(0), true)
	f.Add([]byte("zqx\x00"), "zqx name", "zqx", "zqx group", "zqx", int64(-1), false)
	f.Add([]byte{}, "AWS.x", "", "", "", int64(time.Hour), true)

	f.Fuzz(
		func(t *testing.T, payload []byte, name, value, group, dedupe string, delay int64, fifo bool) {
			id := "q"
			if fifo {
				id = "q.fifo"
			}

			q, err := queue.ParseQueue(id)
			require.NoError(t, err)

			m := queue.Message{
				Attributes: map[string]string{name: value},
				GroupID:    group,
				DedupeID:   dedupe,
				Payload:    payload,
				Delay:      time.Duration(delay),
			}

			err = queue.CheckMessage(q, m)
			assert.Equal(t, err, queue.CheckMessage(q, m))

			if err != nil {
				assert.Regexp(t, errShape, err.Error())
			}
		},
	)
}
