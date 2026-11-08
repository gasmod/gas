package conformance

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
)

// Store is a queue store with a lifecycle and the visibility calls every
// store of the module has.
type Store interface {
	gas.Service
	gas.JobQueueProvider
	// NackAfter makes the job's message visible after d.
	NackAfter(ctx context.Context, queue string, job gas.Job, d time.Duration) error
	// Extend keeps the job's message hidden for d more.
	Extend(ctx context.Context, queue string, job gas.Job, d time.Duration) error
}

// Kind is the kind of queue a rule asks for.
type Kind int

// The kinds of queue.
const (
	// Standard is a standard queue.
	Standard Kind = iota
	// FIFO is a FIFO queue without content-based deduplication.
	FIFO
	// FIFOContentDedupe is a FIFO queue with content-based deduplication.
	FIFOContentDedupe
)

// Subject is a store under test.
type Subject struct {
	// New returns a new store, not initialized, whose VisibilityTimeout
	// setting is visibility.
	New func(t *testing.T, visibility time.Duration) Store
	// CreateQueue creates a new queue of kind, with no other message and
	// a visibility timeout of 30s, for st, which is initialized, and
	// returns its identifier.
	CreateQueue func(t *testing.T, st Store, kind Kind) string
	// Skip maps the name of a rule to the reason the subject skips it,
	// such as an emulator that does not do as SQS does.
	Skip map[string]string
	// Name is the store's ServiceName, which starts its errors.
	Name string
}

// The times the rules wait on.
const (
	// long is a wait for a message that is there or about to be.
	long = 5 * time.Second
	// settle is how long a message stays hidden in the rules that wait
	// for one to come back.
	settle = time.Second
	// prompt is how long a call that should return at once may take.
	prompt = 2 * time.Second
	// closeWait is the wait of the Dequeue that Close ends.
	closeWait = 3 * time.Second
	// pause is how long a rule lets a call it started begin.
	pause = 300 * time.Millisecond
	// longer is a wait no rule sees to its end.
	longer = 10 * time.Second
	// longest bounds the rule that drains a queue many goroutines share.
	longest = 30 * time.Second
)

// batch is how many messages the rules that need several send.
const batch = 5

// The operations errors name.
const (
	opEnqueue = "enqueue"
	opDequeue = "dequeue"
	opAck     = "ack"
	opNack    = "nack"
	opExtend  = "extend"
)

// Run runs the suite on s.
func Run(t *testing.T, s Subject) {
	t.Helper()

	rules := map[string]func(*testing.T, Subject){
		"RoundTrip":            roundTrip,
		"Nack":                 nack,
		"NackAfter":            nackAfter,
		"Extend":               extend,
		"VisibilityExpiry":     visibilityExpiry,
		"Delay":                delay,
		"ShortPoll":            shortPoll,
		"LongPollWakes":        longPollWakes,
		"LongPollEnds":         longPollEnds,
		"MaxMessages":          maxMessages,
		"CanceledContext":      canceledContext,
		"CancelDuringPoll":     cancelDuringPoll,
		"QueueNotFound":        queueNotFound,
		"InvalidQueue":         invalidQueue,
		"Payload":              payload,
		"PayloadSize":          payloadSize,
		"Attributes":           attributes,
		"LongAttributeName":    longAttributeName,
		"DelayBounds":          delayBounds,
		"FIFOOrder":            fifoOrder,
		"FIFOGroupBlocks":      fifoGroupBlocks,
		"FIFORules":            fifoRules,
		"Dedupe":               dedupe,
		"ContentDedupe":        contentDedupe,
		"GroupOnStandard":      groupOnStandard,
		"DequeueArguments":     dequeueArguments,
		"Receipt":              receipt,
		"AckTwice":             ackTwice,
		"Lifecycle":            lifecycle,
		"InitCanceled":         initCanceled,
		"CloseEndsDequeue":     closeEndsDequeue,
		"Concurrent":           concurrent,
		"ErrorsHoldNoMessages": errorsHoldNoMessages,
	}

	for name, rule := range rules {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if reason, ok := s.Skip[name]; ok {
				t.Skip(reason)
			}

			rule(t, s)
		})
	}
}

// open returns a new initialized store of s with visibility, closed when
// the test ends.
func open(t *testing.T, s Subject, visibility time.Duration) Store {
	t.Helper()

	st := s.New(t, visibility)
	require.NoError(t, st.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, st.Close(context.Background())) })

	return st
}

// openQueue returns an initialized store of s with visibility and a new
// queue of kind on it.
func openQueue(t *testing.T, s Subject, visibility time.Duration, kind Kind) (Store, string) {
	t.Helper()

	st := open(t, s, visibility)

	return st, s.CreateQueue(t, st, kind)
}

// receive returns the jobs of one Dequeue of up to maxMessages with wait.
func receive(t *testing.T, st Store, q string, maxMessages int, wait time.Duration) []gas.Job {
	t.Helper()

	jobs, err := st.Dequeue(t.Context(), q, maxMessages, wait)
	require.NoError(t, err)

	return jobs
}

// receiveOne returns the one job a Dequeue of one message, waiting long,
// returns.
func receiveOne(t *testing.T, st Store, q string) gas.Job {
	t.Helper()

	jobs := receive(t, st, q, 1, long)
	require.Len(t, jobs, 1)

	return jobs[0]
}

// requireEmpty checks that a short poll of q returns nothing.
func requireEmpty(t *testing.T, st Store, q string) {
	t.Helper()

	assert.Empty(t, receive(t, st, q, queue.MaxMessages, 0))
}

// name returns the queue name of the identifier q.
func name(t *testing.T, q string) string {
	t.Helper()

	parsed, err := queue.ParseQueue(q)
	require.NoError(t, err)

	return parsed.Name
}

// prefix returns the start of the errors of op on q.
func prefix(t *testing.T, s Subject, op, q string) string {
	t.Helper()

	return s.Name + ": " + op + " " + strconv.Quote(name(t, q)) + ": "
}

// roundTrip checks that a message comes back with its payload, its
// attributes and the system attributes, and that Ack deletes it.
func roundTrip(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()
	before := time.Now().Add(-time.Minute).UnixMilli()

	own := map[string]string{"kind": "greeting", "Trace-ID": "t.1"}
	system := []string{
		queue.AttributeReceiveCount,
		queue.AttributeFirstReceiveTimestamp,
		queue.AttributeSentTimestamp,
	}

	require.NoError(t, st.Enqueue(ctx, q, []byte("héllo\tworld"), gas.WithJobAttributes(own)))

	j := receiveOne(t, st, q)
	assert.Equal(t, "héllo\tworld", string(j.Body))
	assert.NotEmpty(t, j.ID)
	assert.NotEmpty(t, j.ReceiptHandle)
	assert.Equal(t, "greeting", j.Attributes["kind"])
	assert.Equal(t, "t.1", j.Attributes["Trace-ID"])
	assert.Equal(t, "1", j.Attributes[queue.AttributeReceiveCount])

	for _, a := range system[1:] {
		ms, err := strconv.ParseInt(j.Attributes[a], 10, 64)
		require.NoError(t, err, a)
		assert.Greater(t, ms, before, a)
	}

	assert.NotContains(t, j.Attributes, queue.AttributeGroupID)
	assert.NotContains(t, j.Attributes, queue.AttributeSequenceNumber)
	assert.Len(t, j.Attributes, len(own)+len(system))

	require.NoError(t, st.Ack(ctx, q, j))
	assert.Empty(t, receive(t, st, q, 1, settle))
}

// nack checks that Nack makes the message visible at once, received a
// second time with a new receipt handle.
func nack(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("again")))

	first := receiveOne(t, st, q)
	require.NoError(t, st.Nack(ctx, q, first))

	second := receiveOne(t, st, q)
	assert.Equal(t, first.ID, second.ID)
	assert.NotEqual(t, first.ReceiptHandle, second.ReceiptHandle)
	assert.Equal(t, "2", second.Attributes[queue.AttributeReceiveCount])
	assert.Equal(t, first.Attributes[queue.AttributeFirstReceiveTimestamp],
		second.Attributes[queue.AttributeFirstReceiveTimestamp])
	require.NoError(t, st.Ack(ctx, q, second))
}

// nackAfter checks that NackAfter hides the message for its delay, then
// makes it visible.
func nackAfter(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("later")))

	j := receiveOne(t, st, q)
	start := time.Now()

	require.NoError(t, st.NackAfter(ctx, q, j, settle+settle))
	requireEmpty(t, st, q)

	again := receiveOne(t, st, q)
	assert.Equal(t, j.ID, again.ID)
	assert.GreaterOrEqual(t, time.Since(start), settle)
}

// extend checks that Extend keeps a message hidden past the visibility
// timeout it was received with.
func extend(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, settle, Standard)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("long job")))

	j := receiveOne(t, st, q)
	require.NoError(t, st.Extend(ctx, q, j, settle+settle+settle))

	time.Sleep(settle + settle/2)
	requireEmpty(t, st, q)

	again := receiveOne(t, st, q)
	assert.Equal(t, j.ID, again.ID)
}

// visibilityExpiry checks that a message received and not acked comes
// back when the VisibilityTimeout setting ends.
func visibilityExpiry(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, settle, Standard)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("lost")))

	j := receiveOne(t, st, q)
	requireEmpty(t, st, q)

	again := receiveOne(t, st, q)
	assert.Equal(t, j.ID, again.ID)
	assert.Equal(t, "2", again.Attributes[queue.AttributeReceiveCount])
}

// delay checks that a delayed message is hidden until its delay ends,
// and that a delay under a second is rounded up to one.
func delay(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("soon"), gas.WithDelay(time.Millisecond)))
	requireEmpty(t, st, q)

	j := receiveOne(t, st, q)
	assert.Equal(t, "soon", string(j.Body))
}

// shortPoll checks that a Dequeue with no wait returns at once from an
// empty queue, with no jobs.
func shortPoll(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	start := time.Now()

	requireEmpty(t, st, q)
	assert.Less(t, time.Since(start), prompt)
}

// longPollWakes checks that a waiting Dequeue returns a message sent
// while it waits, before its wait ends.
func longPollWakes(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	got := make(chan []gas.Job, 1)
	start := time.Now()

	go func() {
		jobs, err := st.Dequeue(context.Background(), q, 1, longer)
		assert.NoError(t, err)

		got <- jobs
	}()

	time.Sleep(pause)
	require.NoError(t, st.Enqueue(t.Context(), q, []byte("wake")))

	jobs := <-got
	require.Len(t, jobs, 1)
	assert.Equal(t, "wake", string(jobs[0].Body))
	assert.Less(t, time.Since(start), long)
}

// longPollEnds checks that a Dequeue of an empty queue waits for its
// wait, then returns no jobs.
func longPollEnds(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	start := time.Now()

	assert.Empty(t, receive(t, st, q, 1, settle))
	assert.GreaterOrEqual(t, time.Since(start), settle-100*time.Millisecond)
}

// maxMessages checks that a Dequeue returns no more messages than asked.
func maxMessages(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	for i := range batch {
		require.NoError(t, st.Enqueue(ctx, q, []byte(strconv.Itoa(i))))
	}

	jobs := receive(t, st, q, batch-1, long)
	assert.NotEmpty(t, jobs)
	assert.LessOrEqual(t, len(jobs), batch-1)
}

// canceledContext checks that every method returns the context's error
// when its context has ended, and sends nothing.
func canceledContext(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	job := gas.Job{ReceiptHandle: "r"}
	errs := map[string]error{
		opEnqueue: st.Enqueue(ctx, q, []byte("v")),
		opAck:     st.Ack(ctx, q, job),
		opNack:    st.Nack(ctx, q, job),
		opExtend:  st.Extend(ctx, q, job, time.Second),
	}

	_, errs[opDequeue] = st.Dequeue(ctx, q, 1, 0)

	for op, err := range errs {
		require.ErrorIs(t, err, context.Canceled, op)
		require.EqualError(t, err, prefix(t, s, op, q)+"context canceled")
	}

	requireEmpty(t, st, q)
}

// cancelDuringPoll checks that a Dequeue whose context ends while it
// waits returns the context's error at once.
func cancelDuringPoll(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)

	ctx, cancel := context.WithTimeout(t.Context(), pause)
	defer cancel()

	start := time.Now()

	_, err := st.Dequeue(ctx, q, 1, longer)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, prefix(t, s, opDequeue, q)+"context deadline exceeded")
	assert.Less(t, time.Since(start), prompt)
}

// queueNotFound checks that every method on a queue that does not exist
// returns queue.ErrQueueNotFound, naming the queue.
func queueNotFound(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	missing := q + "-missing"
	job := gas.Job{ReceiptHandle: "r"}
	errs := map[string]error{
		opEnqueue: st.Enqueue(t.Context(), missing, []byte("v")),
		opAck:     st.Ack(t.Context(), missing, job),
		opNack:    st.Nack(t.Context(), missing, job),
	}

	_, errs[opDequeue] = st.Dequeue(t.Context(), missing, 1, 0)

	for op, err := range errs {
		require.ErrorIs(t, err, queue.ErrQueueNotFound, op)
		assert.True(
			t,
			strings.HasPrefix(err.Error(), prefix(t, s, op, missing)+"queue does not exist"),
			err,
		)
	}
}

// invalidQueue checks that every method refuses an identifier that is
// neither a queue name nor a queue URL, without quoting it.
func invalidQueue(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	job := gas.Job{ReceiptHandle: "r"}

	for id, text := range map[string]string{
		"":                       "empty",
		"zqx queue":              "not a queue name or URL",
		"http://host/zqx%20name": "not a queue name or URL",
	} {
		errs := map[string]error{
			opEnqueue: st.Enqueue(t.Context(), id, []byte("v")),
			opAck:     st.Ack(t.Context(), id, job),
			opNack:    st.Nack(t.Context(), id, job),
			opExtend:  st.Extend(t.Context(), id, job, 0),
		}

		_, errs[opDequeue] = st.Dequeue(t.Context(), id, 1, 0)

		for op, err := range errs {
			require.ErrorIs(t, err, queue.ErrInvalidQueue, op)
			require.EqualError(t, err, s.Name+": "+op+": invalid queue: "+text)
		}
	}
}

// payload checks the payload rules, which Enqueue checks before it sends
// anything.
func payload(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	p := prefix(t, s, opEnqueue, q)

	for body, text := range map[string]string{
		"":        "invalid payload: empty",
		"ab\xffc": "invalid payload: not valid UTF-8",
		"ab\x00c": "invalid payload: a character SQS does not allow, at byte 2",
		"\x01":    "invalid payload: a character SQS does not allow, at byte 0",
		"ok￾not":  "invalid payload: a character SQS does not allow, at byte 2",
	} {
		err := st.Enqueue(t.Context(), q, []byte(body))
		require.ErrorIs(t, err, queue.ErrInvalidPayload, "%q", body)
		require.EqualError(t, err, p+text)
	}

	err := st.Enqueue(t.Context(), q, nil)
	require.ErrorIs(t, err, queue.ErrInvalidPayload)

	requireEmpty(t, st, q)

	odd := "\t\n\r ퟿�\U00010000\U0010FFFF{}<>&\"'"
	require.NoError(t, st.Enqueue(t.Context(), q, []byte(odd)))
	assert.Equal(t, odd, string(receiveOne(t, st, q).Body))
}

// payloadSize checks that a message of MaxPayloadSize, attributes
// included, is sent and received whole, and a byte more is refused.
func payloadSize(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	full := strings.Repeat("x", queue.MaxPayloadSize)
	require.NoError(t, st.Enqueue(ctx, q, []byte(full)))

	j := receiveOne(t, st, q)
	assert.Len(t, j.Body, queue.MaxPayloadSize)
	require.NoError(t, st.Ack(ctx, q, j))

	// The attribute counts 1 for its name, 6 for its type, String, and 3
	// for its value.
	attrs := gas.WithJobAttributes(map[string]string{"k": "vvv"})
	require.NoError(t, st.Enqueue(ctx, q, []byte(full[10:]), attrs))

	err := st.Enqueue(ctx, q, []byte(full[9:]), attrs)
	require.ErrorIs(t, err, queue.ErrInvalidPayload)
	require.EqualError(t, err, prefix(t, s, opEnqueue, q)+
		"invalid payload: 1048577 bytes with the attributes, more than 1048576")

	err = st.Enqueue(ctx, q, []byte(full+"x"))
	require.ErrorIs(t, err, queue.ErrInvalidPayload)
}

// attributes checks the attribute rules, which Enqueue checks before it
// sends anything, and that ten attributes come back whole.
func attributes(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	p := prefix(t, s, opEnqueue, q)
	ten := map[string]string{}

	for i := range queue.MaxAttributes {
		ten["a"+strconv.Itoa(i)] = strconv.Itoa(i)
	}

	eleven := map[string]string{"z": "1"}
	maps.Copy(eleven, ten)

	cases := attributeCases(eleven)

	for _, c := range cases {
		err := st.Enqueue(t.Context(), q, []byte("v"), gas.WithJobAttributes(c.attrs))
		require.ErrorIs(t, err, queue.ErrInvalidAttribute, c.text)
		require.EqualError(t, err, p+c.text)
	}

	requireEmpty(t, st, q)

	require.NoError(t, st.Enqueue(t.Context(), q, []byte("ten"), gas.WithJobAttributes(ten)))

	j := receiveOne(t, st, q)
	for k, v := range ten {
		assert.Equal(t, v, j.Attributes[k], k)
	}
}

// attributeCase is attributes that break a rule, and the error's text.
type attributeCase struct {
	// attrs are the attributes.
	attrs map[string]string
	// text is the error's text, after the operation and the queue.
	text string
}

// dotRule is the text of the error of a name with a misplaced `.`.
const dotRule = "invalid attribute: a name starts or ends with '.' or holds '..'"

// attributeCases returns the cases of the attribute rules, with eleven,
// which holds one attribute too many.
func attributeCases(eleven map[string]string) []attributeCase {
	return []attributeCase{
		{eleven, "invalid attribute: 11 attributes, at most 10"},
		{map[string]string{"": "v"}, "invalid attribute: a name is empty or longer than 256 bytes"},
		{
			map[string]string{strings.Repeat("n", queue.MaxAttributeNameLength+1): "v"},
			"invalid attribute: a name is empty or longer than 256 bytes",
		},
		{
			map[string]string{"a b": "v"},
			"invalid attribute: a name holds a character other than letters, digits, '_', '-' and '.'",
		},
		{
			map[string]string{".a": "v"},
			dotRule,
		},
		{
			map[string]string{"a.": "v"},
			dotRule,
		},
		{
			map[string]string{"a..b": "v"},
			dotRule,
		},
		{
			map[string]string{"AWS.x": "v"},
			"invalid attribute: a name starts with the reserved prefix AWS. or Amazon., in any case",
		},
		{
			map[string]string{"amazon.x": "v"},
			"invalid attribute: a name starts with the reserved prefix AWS. or Amazon., in any case",
		},
		{
			map[string]string{"ApproximateReceiveCount": "99"},
			`invalid attribute: "ApproximateReceiveCount" is a system attribute`,
		},
		{map[string]string{"SenderId": "x"}, `invalid attribute: "SenderId" is a system attribute`},
		{map[string]string{"k": ""}, "invalid attribute: a value is empty"},
		{map[string]string{"k": "\xff"}, "invalid attribute: a value is not valid UTF-8"},
		{
			map[string]string{"k": "a\x00"},
			"invalid attribute: a value holds a character SQS does not allow",
		},
	}
}

// longAttributeName checks that an attribute name of
// MaxAttributeNameLength bytes is sent and received.
func longAttributeName(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	name := strings.Repeat("n", queue.MaxAttributeNameLength)

	require.NoError(t, st.Enqueue(t.Context(), q, []byte("long"),
		gas.WithJobAttributes(map[string]string{name: "v"})))
	assert.Equal(t, "v", receiveOne(t, st, q).Attributes[name])
}

// delayBounds checks that a delay must be 0 to 15 minutes.
func delayBounds(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	p := prefix(t, s, opEnqueue, q)

	for _, d := range []time.Duration{-time.Nanosecond, queue.MaxDelay + time.Nanosecond, time.Hour} {
		err := st.Enqueue(t.Context(), q, []byte("v"), gas.WithDelay(d))
		require.ErrorIs(t, err, queue.ErrInvalidArgument, d)
		require.EqualError(t, err, p+"invalid argument: delay must be 0 to 15m0s")
	}

	require.NoError(t, st.Enqueue(t.Context(), q, []byte("v"), gas.WithDelay(queue.MaxDelay)))
	requireEmpty(t, st, q)
}

// fifoOrder checks that a FIFO queue returns a group's messages in the
// order they were sent, with the FIFO system attributes.
func fifoOrder(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, FIFO)
	ctx := t.Context()

	for i := range batch {
		n := strconv.Itoa(i)
		require.NoError(
			t,
			st.Enqueue(ctx, q, []byte(n), gas.WithGroupID("g"), gas.WithDedupeID("d"+n)),
		)
	}

	var (
		got  []string
		seqs []string
	)

	for len(got) < batch {
		jobs := receive(t, st, q, queue.MaxMessages, long)
		require.NotEmpty(t, jobs)

		for _, j := range jobs {
			got = append(got, string(j.Body))
			seqs = append(seqs, j.Attributes[queue.AttributeSequenceNumber])

			assert.Equal(t, "g", j.Attributes[queue.AttributeGroupID])
			assert.Equal(t, "d"+string(j.Body), j.Attributes[queue.AttributeDedupeID])
			require.NoError(t, st.Ack(ctx, q, j))
		}
	}

	assert.Equal(t, []string{"0", "1", "2", "3", "4"}, got)
	assert.Len(t, slices.Compact(slices.Clone(seqs)), batch)
}

// fifoGroupBlocks checks that, while a message of a group is in flight,
// no later message of the group is received, and that a Nack keeps it
// first.
func fifoGroupBlocks(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, FIFO)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("a"), gas.WithGroupID("g"), gas.WithDedupeID("a")))
	require.NoError(t, st.Enqueue(ctx, q, []byte("b"), gas.WithGroupID("g"), gas.WithDedupeID("b")))

	a := receive(t, st, q, 1, long)
	require.Len(t, a, 1)
	assert.Equal(t, "a", string(a[0].Body))
	requireEmpty(t, st, q)

	require.NoError(t, st.Nack(ctx, q, a[0]))

	again := receiveOne(t, st, q)
	assert.Equal(t, "a", string(again.Body))
	require.NoError(t, st.Ack(ctx, q, again))
	assert.Equal(t, "b", string(receiveOne(t, st, q).Body))
}

// fifoRules checks the group ID, dedupe ID and delay rules of FIFO and
// standard queues.
func fifoRules(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	fifo := s.CreateQueue(t, st, FIFO)
	std := s.CreateQueue(t, st, Standard)
	ctx := t.Context()
	idText := " must be 1 to 128 ASCII letters, digits and punctuation"

	cases := []struct {
		q    string
		text string
		opts []gas.EnqueueOption
	}{
		{fifo, "a FIFO queue needs a group ID", []gas.EnqueueOption{gas.WithDedupeID("d")}},
		{fifo, "a FIFO queue takes no per-message delay", []gas.EnqueueOption{
			gas.WithGroupID("g"), gas.WithDedupeID("d"), gas.WithDelay(time.Second),
		}},
		{
			fifo,
			"group ID" + idText,
			[]gas.EnqueueOption{gas.WithGroupID("a b"), gas.WithDedupeID("d")},
		},
		{fifo, "group ID" + idText, []gas.EnqueueOption{
			gas.WithGroupID(strings.Repeat("g", queue.MaxIDLength+1)), gas.WithDedupeID("d"),
		}},
		{
			fifo,
			"dedupe ID" + idText,
			[]gas.EnqueueOption{gas.WithGroupID("g"), gas.WithDedupeID("é")},
		},
		{std, "a standard queue takes no dedupe ID", []gas.EnqueueOption{gas.WithDedupeID("d")}},
		{std, "group ID" + idText, []gas.EnqueueOption{gas.WithGroupID("\t")}},
	}

	for _, c := range cases {
		err := st.Enqueue(ctx, c.q, []byte("v"), c.opts...)
		require.ErrorIs(t, err, queue.ErrInvalidArgument, c.text)
		require.EqualError(t, err, prefix(t, s, opEnqueue, c.q)+"invalid argument: "+c.text)
	}

	punct := "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

	punct += strings.Repeat("x", queue.MaxIDLength-len(punct))
	require.NoError(
		t,
		st.Enqueue(ctx, fifo, []byte("v"), gas.WithGroupID(punct), gas.WithDedupeID(punct)),
	)

	j := receiveOne(t, st, fifo)
	assert.Equal(t, punct, j.Attributes[queue.AttributeGroupID])
	assert.Equal(t, punct, j.Attributes[queue.AttributeDedupeID])
}

// dedupe checks that a FIFO queue drops a message whose dedupe ID it
// saw within the dedupe window, and accepts the Enqueue.
func dedupe(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, FIFO)
	ctx := t.Context()

	for _, body := range []string{"first", "second"} {
		require.NoError(
			t,
			st.Enqueue(ctx, q, []byte(body), gas.WithGroupID("g"), gas.WithDedupeID("same")),
		)
	}

	j := receiveOne(t, st, q)
	assert.Equal(t, "first", string(j.Body))
	require.NoError(t, st.Ack(ctx, q, j))

	require.NoError(
		t,
		st.Enqueue(ctx, q, []byte("third"), gas.WithGroupID("g"), gas.WithDedupeID("same")),
	)
	assert.Empty(t, receive(t, st, q, 1, settle))
}

// contentDedupe checks that a FIFO queue with content-based
// deduplication drops a message with the same payload, and that a queue
// without it refuses a message with no dedupe ID, with
// queue.ErrInvalidArgument.
func contentDedupe(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, 0)
	q := s.CreateQueue(t, st, FIFOContentDedupe)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("same"), gas.WithGroupID("g")))
	require.NoError(t, st.Enqueue(ctx, q, []byte("same"), gas.WithGroupID("g")))
	require.NoError(t, st.Enqueue(ctx, q, []byte("other"), gas.WithGroupID("g")))

	var got []string

	for len(got) < 2 {
		j := receiveOne(t, st, q)

		got = append(got, string(j.Body))
		require.NoError(t, st.Ack(ctx, q, j))
	}

	assert.Equal(t, []string{"same", "other"}, got)
	requireEmpty(t, st, q)

	plain := s.CreateQueue(t, st, FIFO)
	err := st.Enqueue(ctx, plain, []byte("v"), gas.WithGroupID("g"))
	require.ErrorIs(t, err, queue.ErrInvalidArgument)
	assert.True(t, strings.HasPrefix(err.Error(), prefix(t, s, opEnqueue, plain)), err)
}

// groupOnStandard checks that a standard queue takes a group ID, as SQS
// takes one for fair queues, and reports it.
func groupOnStandard(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)

	require.NoError(t, st.Enqueue(t.Context(), q, []byte("v"), gas.WithGroupID("tenant-1")))
	assert.Equal(t, "tenant-1", receiveOne(t, st, q).Attributes[queue.AttributeGroupID])
}

// dequeueArguments checks that maxMessages must be 1 to 10 and the wait
// 0 to 20 seconds.
func dequeueArguments(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	p := prefix(t, s, opDequeue, q)

	for _, n := range []int{-1, 0, queue.MaxMessages + 1} {
		_, err := st.Dequeue(t.Context(), q, n, 0)
		require.ErrorIs(t, err, queue.ErrInvalidArgument, n)
		require.EqualError(t, err, p+"invalid argument: maxMessages must be 1 to 10")
	}

	for _, w := range []time.Duration{-time.Nanosecond, queue.MaxWait + time.Nanosecond} {
		_, err := st.Dequeue(t.Context(), q, 1, w)
		require.ErrorIs(t, err, queue.ErrInvalidArgument, w)
		require.EqualError(t, err, p+"invalid argument: wait must be 0 to 20s")
	}
}

// receipt checks that a job without a receipt handle, and a visibility
// timeout out of bounds, are refused before anything is sent.
func receipt(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()
	empty := gas.Job{ID: "x"}
	errs := map[string]error{
		opAck:    st.Ack(ctx, q, empty),
		opNack:   st.Nack(ctx, q, empty),
		opExtend: st.Extend(ctx, q, empty, time.Second),
	}

	for op, err := range errs {
		require.ErrorIs(t, err, queue.ErrInvalidReceipt, op)
		require.EqualError(t, err, prefix(t, s, op, q)+"invalid receipt handle: empty")
	}

	err := st.NackAfter(ctx, q, empty, time.Second)
	require.ErrorIs(t, err, queue.ErrInvalidReceipt)

	job := gas.Job{ReceiptHandle: "r"}

	for _, d := range []time.Duration{-time.Nanosecond, queue.MaxVisibilityTimeout + time.Nanosecond} {
		err = st.NackAfter(ctx, q, job, d)
		require.ErrorIs(t, err, queue.ErrInvalidArgument)
		require.EqualError(
			t,
			err,
			prefix(t, s, opNack, q)+"invalid argument: visibility timeout must be 0 to 12h0m0s",
		)

		err = st.Extend(ctx, q, job, d)
		require.ErrorIs(t, err, queue.ErrInvalidArgument)
		require.EqualError(
			t,
			err,
			prefix(t, s, opExtend, q)+"invalid argument: visibility timeout must be 0 to 12h0m0s",
		)
	}
}

// ackTwice checks that acking a deleted message is not an error.
func ackTwice(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	require.NoError(t, st.Enqueue(ctx, q, []byte("v")))

	j := receiveOne(t, st, q)
	require.NoError(t, st.Ack(ctx, q, j))
	require.NoError(t, st.Ack(ctx, q, j))
}

// lifecycle checks queue.ErrNotInitialized before the first Init,
// queue.ErrAlreadyInitialized from Init on an open store,
// queue.ErrClosed after Close, Close's idempotence, and a restart that
// finds the queue's messages.
func lifecycle(t *testing.T, s Subject) {
	t.Helper()

	st := s.New(t, 0)
	ctx := t.Context()

	requireOps(t, st, queue.ErrNotInitialized, s.Name, "service not initialized")
	require.NoError(t, st.Close(ctx))
	requireOps(t, st, queue.ErrNotInitialized, s.Name, "service not initialized")

	require.NoError(t, st.Init(ctx))

	err := st.Init(ctx)
	require.ErrorIs(t, err, queue.ErrAlreadyInitialized)
	require.EqualError(t, err, s.Name+": init: service already initialized")

	q := s.CreateQueue(t, st, Standard)
	require.NoError(t, st.Enqueue(ctx, q, []byte("kept")))

	require.NoError(t, st.Close(ctx))
	requireOps(t, st, queue.ErrClosed, s.Name, "service closed")
	require.NoError(t, st.Close(ctx))
	requireOps(t, st, queue.ErrClosed, s.Name, "service closed")

	require.NoError(t, st.Init(ctx))
	t.Cleanup(func() { require.NoError(t, st.Close(context.Background())) })

	assert.Equal(t, "kept", string(receiveOne(t, st, q).Body))
}

// requireOps checks that each method of st fails with sentinel, its text
// after the operation and the queue name.
func requireOps(t *testing.T, st Store, sentinel error, name, text string) {
	t.Helper()

	ctx := t.Context()
	job := gas.Job{ReceiptHandle: "r"}
	errs := map[string]error{
		opEnqueue: st.Enqueue(ctx, "q", []byte("v")),
		opAck:     st.Ack(ctx, "q", job),
		opNack:    st.Nack(ctx, "q", job),
		opExtend:  st.Extend(ctx, "q", job, time.Second),
	}

	_, errs[opDequeue] = st.Dequeue(ctx, "q", 1, 0)

	for op, err := range errs {
		require.ErrorIs(t, err, sentinel, op)
		require.EqualError(t, err, name+": "+op+` "q": `+text)
	}

	err := st.NackAfter(ctx, "q", job, time.Second)
	require.ErrorIs(t, err, sentinel)
}

// initCanceled checks that Init with an ended context fails with the
// context's error and leaves the store not initialized.
func initCanceled(t *testing.T, s Subject) {
	t.Helper()

	st := s.New(t, 0)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := st.Init(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, s.Name+": init: context canceled")

	err = st.Enqueue(t.Context(), "q", []byte("v"))
	require.ErrorIs(t, err, queue.ErrNotInitialized)
}

// closeEndsDequeue checks that Close ends a waiting Dequeue with
// queue.ErrClosed, no later than its wait, and returns only after it
// has, and that a message sent after is there after a restart.
func closeEndsDequeue(t *testing.T, s Subject) {
	t.Helper()

	st := s.New(t, 0)
	ctx := t.Context()

	require.NoError(t, st.Init(ctx))

	q := s.CreateQueue(t, st, Standard)
	done := make(chan error, 1)
	start := time.Now()

	go func() {
		_, err := st.Dequeue(context.Background(), q, 1, closeWait)
		done <- err
	}()

	time.Sleep(pause)
	require.NoError(t, st.Close(ctx))

	select {
	case err := <-done:
		require.ErrorIs(t, err, queue.ErrClosed)
		require.EqualError(t, err, prefix(t, s, opDequeue, q)+"service closed")
	default:
		t.Fatal("Close returned before the Dequeue")
	}

	assert.Less(t, time.Since(start), closeWait+prompt)

	require.NoError(t, st.Init(ctx))
	t.Cleanup(func() { require.NoError(t, st.Close(context.Background())) })

	require.NoError(t, st.Enqueue(ctx, q, []byte("after")))
	assert.Equal(t, "after", string(receiveOne(t, st, q).Body))
}

// concurrent sends and receives from several goroutines at once, for
// -race, and checks that every message is received.
func concurrent(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()

	const producers, each = 4, 10

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[string]bool{}
	)

	for p := range producers {
		wg.Go(func() {
			for i := range each {
				assert.NoError(t, st.Enqueue(ctx, q, []byte(strconv.Itoa(p)+":"+strconv.Itoa(i))))
			}
		})
	}

	deadline := time.Now().Add(longest)

	for range 4 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				mu.Lock()

				n := len(seen)
				mu.Unlock()

				if n == producers*each {
					return
				}

				jobs, err := st.Dequeue(ctx, q, queue.MaxMessages, settle)
				if err != nil {
					t.Errorf("dequeue: %v", err)

					return
				}

				for _, j := range jobs {
					mu.Lock()

					seen[string(j.Body)] = true
					mu.Unlock()

					assert.NoError(t, st.Ack(ctx, q, j))
				}
			}
		})
	}

	wg.Wait()
	assert.Len(t, seen, producers*each)
}

// errorsHoldNoMessages checks that no error quotes a payload, an
// attribute name or value, a group ID, a dedupe ID, a receipt handle, or
// the URL of a queue.
func errorsHoldNoMessages(t *testing.T, s Subject) {
	t.Helper()

	st, q := openQueue(t, s, 0, Standard)
	ctx := t.Context()
	url := "https://sqs.eu-west-1.amazonaws.com/123456789012/zqx-missing"

	errs := []error{
		st.Enqueue(ctx, q, []byte("zqxpayload\x00")),
		st.Enqueue(
			ctx,
			q,
			[]byte("v"),
			gas.WithJobAttributes(map[string]string{"zqx name": "zqxvalue"}),
		),
		st.Enqueue(
			ctx,
			q,
			[]byte("v"),
			gas.WithJobAttributes(map[string]string{"k": "zqxvalue\x00"}),
		),
		st.Enqueue(ctx, q, []byte("v"), gas.WithGroupID("zqx group")),
		st.Enqueue(ctx, q, []byte("v"), gas.WithDedupeID("zqxdedupe")),
		st.Ack(ctx, q, gas.Job{ReceiptHandle: "zqxreceipt"}),
		st.Nack(ctx, q, gas.Job{ReceiptHandle: "zqxreceipt"}),
	}

	for _, err := range errs {
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "zqx")
	}

	err := st.Enqueue(ctx, url, []byte("v"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "123456789012")
	assert.NotContains(t, err.Error(), "amazonaws")
	assert.NotErrorIs(t, err, queue.ErrInvalidQueue, err)
}
