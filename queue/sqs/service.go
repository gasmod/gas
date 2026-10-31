package sqs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/internal/configsrc"
)

// ServiceName is the name of the SQS store, returned by Service.Name and
// prefixed to every error the store returns.
const ServiceName = "gas/queue/sqs"

// The operations errors name.
const (
	opInit    = "init"
	opReady   = "ready"
	opEnqueue = "enqueue"
	opDequeue = "dequeue"
	opAck     = "ack"
	opNack    = "nack"
	opExtend  = "extend"
	opClose   = "close"
)

var (
	// ErrNoRegion indicates that Init found no AWS region, in the Region
	// setting or the AWS SDK's default chain.
	ErrNoRegion = errors.New("no region")
	// ErrNoCredentials indicates that Init could not retrieve AWS
	// credentials.
	ErrNoCredentials = errors.New("no credentials")
	// ErrNilClient indicates that WithClient was given a nil client.
	ErrNilClient = errors.New("client is nil")
	// ErrConnect indicates that a request got no reply from SQS: it timed
	// out, its connection was refused, or the endpoint was unreachable, as
	// described in the package documentation.
	ErrConnect = errors.New("no reply")
	// ErrCommand indicates that SQS replied to a request with an error, or
	// that a request failed for another reason than ErrConnect's, as
	// described in the package documentation.
	ErrCommand = errors.New("request failed")
	// ErrAccessDenied indicates that SQS refused the credentials, or their
	// permission for the request. It comes with ErrCommand.
	ErrAccessDenied = errors.New("access denied")
	// ErrThrottled indicates that SQS throttled the request, past the
	// retries. It comes with ErrCommand.
	ErrThrottled = errors.New("throttled")
)

// API is the part of the SQS client the store uses. *sqs.Client
// implements it.
type API interface {
	// SendMessage sends a message, for Enqueue.
	SendMessage(
		ctx context.Context,
		params *awssqs.SendMessageInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.SendMessageOutput, error)
	// ReceiveMessage receives messages, for Dequeue.
	ReceiveMessage(
		ctx context.Context,
		params *awssqs.ReceiveMessageInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.ReceiveMessageOutput, error)
	// DeleteMessage deletes a message, for Ack.
	DeleteMessage(
		ctx context.Context,
		params *awssqs.DeleteMessageInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.DeleteMessageOutput, error)
	// ChangeMessageVisibility changes a message's visibility timeout, for
	// Nack, NackAfter and Extend.
	ChangeMessageVisibility(
		ctx context.Context,
		params *awssqs.ChangeMessageVisibilityInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.ChangeMessageVisibilityOutput, error)
	// ChangeMessageVisibilityBatch changes the visibility timeout of up to
	// ten messages, to make visible again what a Dequeue received but
	// does not return.
	ChangeMessageVisibilityBatch(
		ctx context.Context,
		params *awssqs.ChangeMessageVisibilityBatchInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.ChangeMessageVisibilityBatchOutput, error)
	// GetQueueUrl returns the URL of a queue name.
	GetQueueUrl(
		ctx context.Context,
		params *awssqs.GetQueueUrlInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.GetQueueUrlOutput, error)
}

var _ API = (*awssqs.Client)(nil)

// Service is a queue store on Amazon SQS, as described in the package
// documentation. It is safe for concurrent use.
type Service struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// client is the client given to WithClient.
	client API
	// st is what Init opened, or nil before Init succeeds and after
	// Close. It is swapped whole, so a call sees one Init's state.
	st atomic.Pointer[state]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// clientSet reports whether WithClient was given, so that a nil
	// client is told apart from none.
	clientSet bool
	// closed reports whether Close has ever run after a successful Init.
	// Close sets it before it clears st, and Init never clears it, so a
	// call that finds st nil after a Close finds closed set, even while
	// a restart runs.
	closed atomic.Bool
	// mu makes Init and Close run one at a time. Calls do not take it.
	mu sync.Mutex
}

var (
	_ gas.Service          = (*Service)(nil)
	_ gas.JobQueueProvider = (*Service)(nil)
	_ gas.ReadyReporter    = (*Service)(nil)
)

// state is what one Init opened, with the settings calls use.
type state struct {
	// ctx ends when a Close whose context ended cancels the calls in
	// flight; every request runs within it.
	ctx context.Context //nolint:containedctx // the cancellation of one Init's calls.
	// cancel ends ctx.
	cancel context.CancelFunc
	// client sends the requests.
	client API
	// transport is the HTTP transport of a client Init built, or nil.
	transport *http.Transport
	// urls maps the queue names resolved to their URLs.
	urls map[string]string
	// idle is closed once Close has begun and no call is in flight.
	idle chan struct{}
	// opTimeout is Settings.OpTimeout.
	opTimeout time.Duration
	// visibility is Settings.VisibilityTimeout, rounded up to a whole
	// second.
	visibility time.Duration
	// calls counts the calls in flight, which Close waits for.
	calls int
	// mu guards urls, calls and closing.
	mu sync.Mutex
	// closing reports whether Close began.
	closing bool
}

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
		}
		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves and checks the configuration, builds the client and
// retrieves the credentials, as described in the package documentation.
// On an initialized store it returns queue.ErrAlreadyInitialized and
// changes nothing. A failed Init leaves the store as it was.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.st.Load() != nil {
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

	set := &cfg.Queue

	client := s.client
	region, source := "", ""

	var transport *http.Transport

	switch {
	case s.clientSet && isNil(client):
		return opError(opInit, "", ErrNilClient)
	case !s.clientSet:
		var built *awssqs.Client

		built, transport, source, err = s.build(ctx, set)
		if err != nil {
			return err
		}

		client, region = built, built.Options().Region
	}

	stCtx, cancel := context.WithCancel(context.Background())

	s.st.Store(&state{
		ctx:        stCtx,
		cancel:     cancel,
		idle:       make(chan struct{}),
		client:     client,
		transport:  transport,
		urls:       make(map[string]string),
		opTimeout:  set.OpTimeout,
		visibility: time.Duration(queue.WholeSeconds(set.VisibilityTimeout)) * time.Second,
	})

	s.logInitialized(set, region, source)

	return nil
}

// Close makes every later call but Init return an error wrapping
// queue.ErrClosed, and waits for every call in flight to return, as
// described in the package documentation: a Dequeue in flight finishes
// its receive, makes what it received visible again, and returns
// queue.ErrClosed. When ctx ends first, Close cancels the requests in
// flight, whose calls return queue.ErrClosed, and returns an error
// wrapping ctx's error; the store stays closed. Close then closes the idle
// connections of a client Init built. A Close that runs at the same time
// waits for it too. Close on a store that is not initialized returns nil
// and logs nothing.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.st.Load()
	if st == nil {
		return nil
	}

	st.mu.Lock()

	st.closing = true
	if st.calls == 0 {
		close(st.idle)
	}

	st.mu.Unlock()

	s.closed.Store(true)
	s.st.Store(nil)

	var err error

	select {
	case <-st.idle:
	case <-ctx.Done():
		select {
		case <-st.idle:
		default:
			err = opError(opClose, "", ctx.Err())
		}
	}

	// Cancels the requests still in flight when ctx ended, and else frees
	// the context.
	st.cancel()

	if st.transport != nil {
		st.transport.CloseIdleConnections()
	}

	if err != nil {
		s.logger.Warn("service closed, calls in flight canceled").Err("error", err).Send()

		return err
	}

	s.logger.Info("service closed").Send()

	return nil
}

// CheckReady reports readiness without I/O: an error wrapping
// queue.ErrNotInitialized or queue.ErrClosed when the store is not
// initialized, the context's error when ctx has ended, and else nil.
func (s *Service) CheckReady(ctx context.Context) error {
	st, err := s.begin(ctx, opReady, "")
	if err != nil {
		return err
	}

	st.end()

	return nil
}

// Client returns the *sqs.Client the store sends its requests with, or
// nil when the store is not initialized or was given another API with
// WithClient, as described in the package documentation.
func (s *Service) Client() *awssqs.Client {
	st := s.st.Load()
	if st == nil {
		return nil
	}

	c, _ := st.client.(*awssqs.Client)

	return c
}

// logInitialized logs `service initialized`, as described in the package
// documentation, with the credential source unless a client was given.
func (s *Service) logInitialized(set *Settings, region, source string) {
	event := s.logger.Info("service initialized").
		Str("region", region).
		Bool("endpoint", set.Endpoint != "").
		Bool("client", s.clientSet)
	if !s.clientSet {
		event = event.Str("credentials", source)
	}

	event.Duration("op_timeout", set.OpTimeout).Send()
}

// build returns the client buildClient builds of set, its transport,
// and where it takes its credentials from, as credentialSource tells,
// after it logs a warning naming the keys missing from a partial set of
// static credentials.
func (s *Service) build(
	ctx context.Context,
	set *Settings,
) (*awssqs.Client, *http.Transport, string, error) {
	source, missing := credentialSource(set)
	if len(missing) > 0 {
		s.logger.Warn("static credentials incomplete, using the default chain").
			Str("missing", strings.Join(missing, ", ")).
			Send()
	}

	client, transport, err := buildClient(ctx, set)
	if err != nil {
		return nil, nil, "", err
	}

	return client, transport, source, nil
}

// begin returns the state of an initialized store whose ctx has not
// ended, counted as a call in flight until the caller calls st.end, or
// the error of op on the queue name:
// queue.ErrNotInitialized, queue.ErrClosed or the context's.
func (s *Service) begin(ctx context.Context, op, name string) (*state, error) {
	st := s.st.Load()

	switch {
	case st == nil && s.closed.Load():
		return nil, opError(op, name, queue.ErrClosed)
	case st == nil:
		return nil, opError(op, name, queue.ErrNotInitialized)
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.closing {
		return nil, opError(op, name, queue.ErrClosed)
	}

	err := ctx.Err()
	if err != nil {
		return nil, opError(op, name, err)
	}

	st.calls++

	return st, nil
}

// end counts a call begin returned as no longer in flight.
func (st *state) end() {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.calls--
	if st.closing && st.calls == 0 {
		close(st.idle)
	}
}

// isNil reports whether client is nil, or a nil *sqs.Client.
func isNil(client API) bool {
	c, ok := client.(*awssqs.Client)

	return client == nil || ok && c == nil
}

// isClosing reports whether Close began.
func (st *state) isClosing() bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	return st.closing
}

// opError returns err as the failure of op on the queue name, after
// ServiceName; an empty name is left out.
func opError(op, name string, err error) error {
	return fmt.Errorf("%s: %w", opPrefix(op, name), err)
}

// opPrefix returns the start of the errors of op on the queue name:
// ServiceName, the operation, and the name quoted, unless it is empty.
func opPrefix(op, name string) string {
	if name == "" {
		return ServiceName + ": " + op
	}

	return fmt.Sprintf("%s: %s %q", ServiceName, op, name)
}
