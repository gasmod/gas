package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/internal/configsrc"
)

// ServiceName is the name of the memory store, returned by Service.Name
// and prefixed to every error the store returns.
const ServiceName = "gas/queue/memory"

// The operations errors name.
const (
	opInit    = "init"
	opCreate  = "create queue"
	opEnqueue = "enqueue"
	opDequeue = "dequeue"
	opAck     = "ack"
	opNack    = "nack"
	opExtend  = "extend"
)

var (
	// ErrQueueFull indicates that Enqueue found the queue holding
	// MaxMessages messages, or a FIFO queue remembering MaxMessages
	// dedupe IDs, or that the message would take the queue past MaxBytes.
	ErrQueueFull = errors.New("queue full")
	// ErrQueueExists indicates that CreateQueue found the queue with
	// another configuration.
	ErrQueueExists = errors.New("queue exists with another configuration")
)

// minWake is the shortest a waiting Dequeue sleeps before it looks at its
// queue again, so that a clock that does not move cannot make it spin.
const minWake = time.Millisecond

// Service is a queue store held in the process's memory, as described in
// the package documentation. It is safe for concurrent use.
type Service struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// clock tells the time messages are timed by.
	clock Clock
	// queues holds the queues by identifier. They outlive Close.
	queues map[string]*mqueue
	// sess is what Init opened, or nil before Init succeeds and after
	// Close.
	sess *session
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// seq is the last message seq given.
	seq uint64
	// closed reports whether Close ran after a successful Init, and Init
	// has not succeeded since.
	closed bool
	// mu guards queues, sess, seq, closed and every queue. Each method
	// holds it while it works, which does no I/O; a waiting Dequeue
	// releases it while it waits.
	mu sync.Mutex
}

// session is what one Init opened.
type session struct {
	// done is closed by Close, which ends every waiting Dequeue.
	done chan struct{}
	// waiting counts the Dequeues that wait, which Close waits for.
	waiting sync.WaitGroup
	// settings is the resolved configuration.
	settings Settings
}

var (
	_ gas.Service          = (*Service)(nil)
	_ gas.JobQueueProvider = (*Service)(nil)
)

// New returns the constructor of a Service with opts applied, in the shape
// the gas container expects. The constructor returns an error wrapping
// queue.ErrNilLogger for a nil logger; a nil ConfigProvider is accepted,
// and Init then requires WithConfig.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, queue.ErrNilLogger)
		}

		s := &Service{
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
			queues:      make(map[string]*mqueue),
		}
		for _, opt := range opts {
			opt(s)
		}

		if s.clock == nil {
			s.clock = systemClock{}
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves the configuration and opens the store, as described in
// the package documentation. On an initialized store it returns
// queue.ErrAlreadyInitialized and changes nothing.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sess != nil {
		return opError(opInit, "", queue.ErrAlreadyInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return opError(opInit, "", err)
	}

	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, rules)
	if err != nil {
		return opError(opInit, "", err)
	}

	set := cfg.Queue

	set.VisibilityTimeout = roundUp(set.VisibilityTimeout)

	s.sess = &session{done: make(chan struct{}), settings: set}
	s.closed = false

	s.logger.Info("service initialized").
		Int("max_messages", set.MaxMessages).
		Int("max_bytes", set.MaxBytes).
		Duration("visibility_timeout", set.VisibilityTimeout).
		Send()

	return nil
}

// Close makes every later call but Init return an error wrapping
// queue.ErrClosed, ends the Dequeues that wait, and waits for them to
// return. The queues and their messages stay. Close on a store that is
// not initialized returns nil and logs nothing. It ignores its context.
func (s *Service) Close(context.Context) error {
	s.mu.Lock()

	sess := s.sess
	if sess != nil {
		s.sess = nil
		s.closed = true

		close(sess.done)
	}

	s.mu.Unlock()

	if sess == nil {
		return nil
	}

	sess.waiting.Wait()

	s.logger.Info("service closed").Send()

	return nil
}

// CreateQueue creates the queue id with cfg, as described in the package
// documentation. Creating a queue that exists with the same
// configuration is not an error; with another, it is an error wrapping
// ErrQueueExists.
func (s *Service) CreateQueue(ctx context.Context, id string, cfg QueueConfig) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(opCreate, "", err)
	}

	cfg, err = checkQueueConfig(q, cfg)
	if err != nil {
		return opError(opCreate, q.Name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.open(ctx, opCreate, q.Name)
	if err != nil {
		return err
	}

	mq, ok := s.queues[id]
	if !ok {
		s.queues[id] = newQueue(q, cfg)

		return nil
	}

	if mq.cfg != cfg {
		return opError(opCreate, q.Name, ErrQueueExists)
	}

	return nil
}

// Enqueue sends payload to the queue with opts, as described in the
// package documentation. On a FIFO queue, a message whose dedupe ID the
// queue remembers is accepted and not stored.
func (s *Service) Enqueue(
	ctx context.Context,
	id string,
	payload []byte,
	opts ...gas.EnqueueOption,
) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(opEnqueue, "", err)
	}

	m := queue.NewMessage(payload, opts)

	err = queue.CheckMessage(q, m)
	if err != nil {
		return opError(opEnqueue, q.Name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.open(ctx, opEnqueue, q.Name)
	if err != nil {
		return err
	}

	mq, err := s.lookup(opEnqueue, q)
	if err != nil {
		return err
	}

	now := s.clock.Now()

	var dedupe string

	if q.FIFO {
		dedupe = mq.dedupeKey(m)
		if dedupe == "" {
			return opError(opEnqueue, q.Name, errNoDedupeID)
		}

		if mq.remembers(dedupe, now) {
			return nil
		}
	}

	size := messageSize(m)
	if full(mq, &sess.settings, size) {
		return opError(opEnqueue, q.Name, ErrQueueFull)
	}

	delay := roundUp(m.Delay)
	if delay == 0 {
		delay = mq.cfg.Delay
	}

	s.seq++
	mq.add(m, size, dedupe, s.seq, now, delay)

	return nil
}

// full reports whether mq, under set, has no room for a message of size
// bytes: it holds MaxMessages messages, or remembers MaxMessages dedupe
// IDs once those past the window are forgotten, or the message would
// take it past MaxBytes.
func full(mq *mqueue, set *Settings, size int) bool {
	return mq.msgs.Len() >= set.MaxMessages ||
		len(mq.dedupeOrder) >= set.MaxMessages ||
		mq.size+size > set.MaxBytes
}

// Dequeue receives up to maxMessages messages of the queue, waiting up
// to wait for the first, as described in the package documentation.
func (s *Service) Dequeue(
	ctx context.Context,
	id string,
	maxMessages int,
	wait time.Duration,
) ([]gas.Job, error) {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return nil, opError(opDequeue, "", err)
	}

	err = queue.CheckDequeue(maxMessages, wait)
	if err != nil {
		return nil, opError(opDequeue, q.Name, err)
	}

	deadline := time.Now().Add(roundUp(wait))

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.open(ctx, opDequeue, q.Name)
	if err != nil {
		return nil, err
	}

	mq, err := s.lookup(opDequeue, q)
	if err != nil {
		return nil, err
	}

	vis := sess.settings.VisibilityTimeout
	if vis == 0 {
		vis = mq.cfg.VisibilityTimeout
	}

	jobs, next := mq.receive(s.clock.Now(), maxMessages, vis)
	if len(jobs) > 0 || wait == 0 {
		return jobs, nil
	}

	return s.await(ctx, sess, mq, maxMessages, vis, deadline, next)
}

// Ack deletes the job's message when job holds its latest receipt
// handle. A stale handle, or one of a message already deleted, deletes
// nothing and is not an error.
func (s *Service) Ack(ctx context.Context, id string, job gas.Job) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(opAck, "", err)
	}

	err = queue.CheckReceipt(job)
	if err != nil {
		return opError(opAck, q.Name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.open(ctx, opAck, q.Name)
	if err != nil {
		return err
	}

	mq, err := s.lookup(opAck, q)
	if err != nil {
		return err
	}

	m, ok := mq.find(job.ReceiptHandle)
	if !ok {
		return opError(opAck, q.Name, errForeignReceipt)
	}

	if m != nil {
		mq.remove(m)
	}

	return nil
}

// Nack makes the job's message visible now, as NackAfter with 0.
func (s *Service) Nack(ctx context.Context, id string, job gas.Job) error {
	return s.changeVisibility(ctx, opNack, id, job, 0)
}

// NackAfter makes the job's message visible after d, 0 to 12h, rounded
// up to a whole second, counted from now, as described in the package
// documentation.
func (s *Service) NackAfter(ctx context.Context, id string, job gas.Job, d time.Duration) error {
	return s.changeVisibility(ctx, opNack, id, job, d)
}

// Extend keeps the job's message hidden for d more, 0 to 12h, rounded up
// to a whole second, counted from now, as described in the package
// documentation.
func (s *Service) Extend(ctx context.Context, id string, job gas.Job, d time.Duration) error {
	return s.changeVisibility(ctx, opExtend, id, job, d)
}

// await waits on mq, within deadline, for up to maxMessages messages to
// lease for vis, as described in the package documentation, looking
// again when the clock reaches next, unless it is zero. The caller holds
// s.mu, which await releases while it waits.
func (s *Service) await(
	ctx context.Context,
	sess *session,
	mq *mqueue,
	maxMessages int,
	vis time.Duration,
	deadline, next time.Time,
) ([]gas.Job, error) {
	sess.waiting.Add(1)
	defer sess.waiting.Done()

	for {
		err := s.wait(ctx, sess, mq.changed, deadline, next)
		if err == nil && s.sess != sess {
			// The wait ended for another reason as Close ran.
			err = queue.ErrClosed
		}

		if err != nil {
			return nil, opError(opDequeue, mq.q.Name, err)
		}

		var jobs []gas.Job

		jobs, next = mq.receive(s.clock.Now(), maxMessages, vis)
		if len(jobs) > 0 || !time.Now().Before(deadline) {
			return jobs, nil
		}
	}
}

// wait releases s.mu until changed is closed, deadline passes, or the
// clock reaches next, unless it is zero, then takes it again. It returns
// ctx's error when ctx ends, and queue.ErrClosed when Close ends sess,
// first.
func (s *Service) wait(
	ctx context.Context,
	sess *session,
	changed <-chan struct{},
	deadline, next time.Time,
) error {
	d := time.Until(deadline)
	if !next.IsZero() {
		d = min(d, next.Sub(s.clock.Now()))
	}

	timer := time.NewTimer(max(d, minWake))
	defer timer.Stop()

	s.mu.Unlock()
	defer s.mu.Lock()

	select {
	case <-ctx.Done():
		//nolint:wrapcheck // Dequeue wraps it with the operation.
		return ctx.Err()
	case <-sess.done:
		return queue.ErrClosed
	case <-changed:
	case <-timer.C:
	}

	return nil
}

// changeVisibility makes the job's message visible after d, for op.
func (s *Service) changeVisibility(
	ctx context.Context,
	op, id string,
	job gas.Job,
	d time.Duration,
) error {
	q, err := queue.ParseQueue(id)
	if err != nil {
		return opError(op, "", err)
	}

	err = queue.CheckReceipt(job)
	if err == nil {
		err = queue.CheckVisibilityTimeout(d)
	}

	if err != nil {
		return opError(op, q.Name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.open(ctx, op, q.Name)
	if err != nil {
		return err
	}

	mq, err := s.lookup(op, q)
	if err != nil {
		return err
	}

	m, ok := mq.find(job.ReceiptHandle)
	if !ok {
		return opError(op, q.Name, errForeignReceipt)
	}

	now := s.clock.Now()
	if m == nil || !m.visibleAt.After(now) {
		return opError(op, q.Name, queue.ErrNotInFlight)
	}

	visibleAt := now.Add(roundUp(d))
	if visibleAt.After(m.receivedAt.Add(queue.MaxVisibilityTimeout)) {
		return opError(op, q.Name, errPastLease)
	}

	m.visibleAt = visibleAt

	mq.notify()

	return nil
}

// errNoDedupeID reports a FIFO message without a dedupe ID for a queue
// without content-based deduplication.
var errNoDedupeID = fmt.Errorf("%w: a dedupe ID is needed without content-based deduplication",
	queue.ErrInvalidArgument)

// errForeignReceipt reports a receipt handle the store did not issue.
var errForeignReceipt = fmt.Errorf("%w: not one this store issued", queue.ErrInvalidReceipt)

// errPastLease reports a visibility timeout that would end more than
// MaxVisibilityTimeout after the receive.
var errPastLease = fmt.Errorf("%w: visibility timeout must end within %v of the receive",
	queue.ErrInvalidArgument, queue.MaxVisibilityTimeout)

// open returns the session of an initialized store whose ctx has not
// ended, or the error of op on the queue name: queue.ErrNotInitialized,
// queue.ErrClosed or the context's. The caller holds s.mu.
func (s *Service) open(ctx context.Context, op, name string) (*session, error) {
	switch {
	case s.sess == nil && s.closed:
		return nil, opError(op, name, queue.ErrClosed)
	case s.sess == nil:
		return nil, opError(op, name, queue.ErrNotInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return nil, opError(op, name, err)
	}

	return s.sess, nil
}

// lookup returns the queue of q, or the error of op wrapping
// queue.ErrQueueNotFound. The caller holds s.mu.
func (s *Service) lookup(op string, q queue.Queue) (*mqueue, error) {
	mq, ok := s.queues[q.ID]
	if !ok {
		return nil, opError(op, q.Name, queue.ErrQueueNotFound)
	}

	return mq, nil
}

// checkQueueConfig returns cfg with its defaults applied and its
// durations rounded up to a whole second, or an error wrapping
// queue.ErrInvalidArgument.
func checkQueueConfig(q queue.Queue, cfg QueueConfig) (QueueConfig, error) {
	switch {
	case cfg.VisibilityTimeout < 0 || cfg.VisibilityTimeout > queue.MaxVisibilityTimeout:
		return cfg, fmt.Errorf("%w: visibility timeout must be 0 to %v", queue.ErrInvalidArgument,
			queue.MaxVisibilityTimeout)
	case cfg.Delay < 0 || cfg.Delay > queue.MaxDelay:
		return cfg, fmt.Errorf(
			"%w: delay must be 0 to %v",
			queue.ErrInvalidArgument,
			queue.MaxDelay,
		)
	case cfg.ContentBasedDeduplication && !q.FIFO:
		return cfg, fmt.Errorf("%w: a standard queue takes no content-based deduplication",
			queue.ErrInvalidArgument)
	}

	if cfg.VisibilityTimeout == 0 {
		cfg.VisibilityTimeout = DefaultQueueVisibilityTimeout
	}

	cfg.VisibilityTimeout = roundUp(cfg.VisibilityTimeout)
	cfg.Delay = roundUp(cfg.Delay)

	return cfg, nil
}

// roundUp returns d rounded up to a whole second, as queue.WholeSeconds
// counts it.
func roundUp(d time.Duration) time.Duration {
	return time.Duration(queue.WholeSeconds(d)) * time.Second
}

// opError returns err as the failure of op on the queue name, after
// ServiceName; an empty name is left out.
func opError(op, name string, err error) error {
	if name == "" {
		return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
	}

	return fmt.Errorf("%s: %s %q: %w", ServiceName, op, name, err)
}
