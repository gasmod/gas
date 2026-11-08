package memory

import (
	"container/list"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
)

// QueueConfig holds the settings of one queue, given to CreateQueue, as
// described in the package documentation.
type QueueConfig struct {
	// VisibilityTimeout is how long a received message stays hidden when
	// the store's VisibilityTimeout setting is 0: 1s to 12h, or 0 for
	// DefaultQueueVisibilityTimeout. It is rounded up to a whole second.
	VisibilityTimeout time.Duration
	// Delay is how long a message sent without a delay of its own stays
	// hidden, 0 to 15m. It is rounded up to a whole second.
	Delay time.Duration
	// ContentBasedDeduplication makes a FIFO queue take the SHA-256 of a
	// payload as its dedupe ID when Enqueue is given none.
	ContentBasedDeduplication bool
}

// message is one message of a queue.
type message struct {
	// attrs are the user attributes, the store's own copy.
	attrs map[string]string
	// sentAt is when it was sent.
	sentAt time.Time
	// visibleAt is when it becomes visible.
	visibleAt time.Time
	// firstReceivedAt is when it was first received, or the zero time.
	firstReceivedAt time.Time
	// receivedAt is when it was last received, or the zero time.
	receivedAt time.Time
	// id is the message ID.
	id string
	// group is the group ID, or empty.
	group string
	// dedupe is the dedupe ID of a FIFO message, given or computed.
	dedupe string
	// receipt is the receipt handle of the latest receive, or empty.
	receipt string
	// body is the payload, the store's own copy.
	body []byte
	// seq tells the message apart from every other of the store; receipt
	// handles hold it.
	seq uint64
	// fifoSeq is the sequence number of a FIFO message.
	fifoSeq uint64
	// receives counts the receives.
	receives int
	// size is the bytes it counts toward MaxBytes.
	size int
}

// dedupeEntry is a dedupe ID a FIFO queue remembers.
type dedupeEntry struct {
	// expires is when the queue forgets it.
	expires time.Time
	// id is the dedupe ID.
	id string
}

// mqueue is one queue and its messages. Service.mu guards it.
type mqueue struct {
	// bySeq maps each message's seq to its element of msgs.
	bySeq map[uint64]*list.Element
	// dedupes holds the dedupe IDs remembered, by ID.
	dedupes map[string]bool
	// msgs holds every *message, in the order they were sent.
	msgs *list.List
	// changed is closed, and replaced, when a message may have become
	// available, which wakes the Dequeues that wait.
	changed chan struct{}
	// dedupeOrder holds the dedupe IDs remembered, oldest first.
	dedupeOrder []dedupeEntry
	// q is the parsed identifier.
	q queue.Queue
	// cfg is the queue's configuration, its durations rounded.
	cfg QueueConfig
	// fifoSeq is the last sequence number given.
	fifoSeq uint64
	// size is the bytes its messages count toward MaxBytes.
	size int
}

// newQueue returns an empty queue of q with cfg.
func newQueue(q queue.Queue, cfg QueueConfig) *mqueue {
	return &mqueue{
		bySeq:   make(map[uint64]*list.Element),
		dedupes: make(map[string]bool),
		msgs:    list.New(),
		changed: make(chan struct{}),
		q:       q,
		cfg:     cfg,
	}
}

// notify wakes the Dequeues that wait on mq.
func (mq *mqueue) notify() {
	close(mq.changed)

	mq.changed = make(chan struct{})
}

// dedupeKey returns the dedupe ID of m on a FIFO queue: m's own, or the
// SHA-256 of its payload with content-based deduplication, else "".
func (mq *mqueue) dedupeKey(m queue.Message) string {
	if m.DedupeID != "" {
		return m.DedupeID
	}

	if mq.cfg.ContentBasedDeduplication {
		sum := sha256.Sum256(m.Payload)

		return hex.EncodeToString(sum[:])
	}

	return ""
}

// messageSize returns the bytes m counts toward MaxBytes: its payload,
// and the name and the value of each attribute.
func messageSize(m queue.Message) int {
	n := len(m.Payload)

	for name, value := range m.Attributes {
		n += len(name) + len(value)
	}

	return n
}

// remembers reports whether the queue remembers dedupe at now, first
// forgetting the IDs whose window has passed.
func (mq *mqueue) remembers(dedupe string, now time.Time) bool {
	n := 0
	for n < len(mq.dedupeOrder) && !now.Before(mq.dedupeOrder[n].expires) {
		delete(mq.dedupes, mq.dedupeOrder[n].id)

		n++
	}

	mq.dedupeOrder = mq.dedupeOrder[n:]

	return mq.dedupes[dedupe]
}

// add stores m, of size bytes, sent at now, as message seq, visible
// after delay.
func (mq *mqueue) add(
	m queue.Message,
	size int,
	dedupe string,
	seq uint64,
	now time.Time,
	delay time.Duration,
) {
	msg := &message{
		size:      size,
		attrs:     maps.Clone(m.Attributes),
		id:        rand.Text(),
		group:     m.GroupID,
		dedupe:    dedupe,
		body:      append([]byte{}, m.Payload...),
		sentAt:    now,
		visibleAt: now.Add(delay),
		seq:       seq,
	}

	if mq.q.FIFO {
		mq.fifoSeq++

		msg.fifoSeq = mq.fifoSeq

		mq.dedupes[dedupe] = true
		mq.dedupeOrder = append(
			mq.dedupeOrder,
			dedupeEntry{expires: now.Add(queue.DedupeWindow), id: dedupe},
		)
	}

	mq.size += size

	mq.bySeq[seq] = mq.msgs.PushBack(msg)
	mq.notify()
}

// receive leases up to max available messages at now, each for vis, in
// the order they were sent and, on a FIFO queue, never past a message of
// the same group that is not available. It returns them as jobs, and
// the earliest time a message it passed becomes visible, or the zero
// time.
func (mq *mqueue) receive(
	now time.Time,
	maxMessages int,
	vis time.Duration,
) ([]gas.Job, time.Time) {
	var (
		jobs    []gas.Job
		next    time.Time
		stopped map[string]bool
	)

	if mq.q.FIFO {
		stopped = make(map[string]bool)
	}

	for el := mq.msgs.Front(); el != nil && len(jobs) < maxMessages; el = el.Next() {
		m := el.Value.(*message) //nolint:forcetypeassert // msgs holds only *message.

		if stopped[m.group] {
			continue
		}

		if m.visibleAt.After(now) {
			if next.IsZero() || m.visibleAt.Before(next) {
				next = m.visibleAt
			}

			if stopped != nil {
				stopped[m.group] = true
			}

			continue
		}

		jobs = append(jobs, mq.lease(m, now, vis))
	}

	return jobs, next
}

// lease marks m received at now and hidden for vis, with a new receipt
// handle, and returns it as a job.
func (mq *mqueue) lease(m *message, now time.Time, vis time.Duration) gas.Job {
	m.receives++

	if m.firstReceivedAt.IsZero() {
		m.firstReceivedAt = now
	}

	m.receivedAt = now
	m.visibleAt = now.Add(vis)
	m.receipt = strconv.FormatUint(m.seq, 10) + ":" + strconv.Itoa(m.receives)

	attrs := maps.Clone(m.attrs)
	if attrs == nil {
		attrs = make(map[string]string, len(queue.SystemAttributes()))
	}

	attrs[queue.AttributeReceiveCount] = strconv.Itoa(m.receives)
	attrs[queue.AttributeFirstReceiveTimestamp] = millis(m.firstReceivedAt)
	attrs[queue.AttributeSentTimestamp] = millis(m.sentAt)

	if m.group != "" {
		attrs[queue.AttributeGroupID] = m.group
	}

	if mq.q.FIFO {
		attrs[queue.AttributeDedupeID] = m.dedupe
		attrs[queue.AttributeSequenceNumber] = strconv.FormatUint(m.fifoSeq, 10)
	}

	return gas.Job{
		ID:            m.id,
		ReceiptHandle: m.receipt,
		Attributes:    attrs,
		Body:          append([]byte{}, m.body...),
	}
}

// find returns the message whose latest receipt handle is receipt, and
// whether the handle is one the store could have issued: it may be
// stale, or its message gone, and then find returns nil.
func (mq *mqueue) find(receipt string) (*message, bool) {
	seqText, n, ok := strings.Cut(receipt, ":")
	if !ok {
		return nil, false
	}

	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil {
		return nil, false
	}

	_, err = strconv.ParseUint(n, 10, 64)
	if err != nil {
		return nil, false
	}

	el, ok := mq.bySeq[seq]
	if !ok {
		return nil, true
	}

	m := el.Value.(*message) //nolint:forcetypeassert // msgs holds only *message.
	if m.receipt != receipt {
		return nil, true
	}

	return m, true
}

// remove deletes m.
func (mq *mqueue) remove(m *message) {
	mq.msgs.Remove(mq.bySeq[m.seq])
	delete(mq.bySeq, m.seq)

	mq.size -= m.size

	// Deleting the head of a FIFO group can make the next one available.
	mq.notify()
}

// millis returns t as milliseconds since the Unix epoch, in decimal.
func millis(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
