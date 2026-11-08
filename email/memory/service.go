package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/configsrc"
	"github.com/gasmod/gas/email/internal/lifecycle"
	"github.com/gasmod/gas/email/internal/render"
)

// ServiceName is the name of the memory sender, returned by Service.Name
// and prefixed to every error the sender returns.
const ServiceName = "gas/email/memory"

// The operations errors name.
const (
	opInit         = "init"
	opReady        = "ready"
	opSend         = "send"
	opSendTemplate = "send from template"
	opClose        = "close"
)

// ErrOutboxFull indicates that a send found the outbox holding
// MaxMessages messages, or that its message would take the outbox past
// MaxBytes.
var ErrOutboxFull = errors.New("outbox full")

// Service is an email sender that keeps what it sends in an outbox in
// the process's memory, as described in the package documentation. It is
// safe for concurrent use.
type Service struct {
	// templates is the TemplateProvider SendFromTemplate fetches from.
	templates gas.TemplateProvider
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the sender's events, with `service` set.
	logger gas.Logger
	// outbox holds the messages sent, in order. It outlives Close.
	outbox []*gas.Email
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// lc is the lifecycle, whose sessions hold the resolved settings.
	lc lifecycle.Lifecycle[Settings]
	// mu guards outbox and outboxBytes.
	mu sync.Mutex
	// outboxBytes is the sum of email.MessageSize over outbox.
	outboxBytes int
}

var (
	_ gas.Service       = (*Service)(nil)
	_ gas.EmailProvider = (*Service)(nil)
	_ gas.ReadyReporter = (*Service)(nil)
)

// New returns the constructor of a Service with opts applied, in the shape
// the gas container expects. The constructor returns an error wrapping
// email.ErrNilTemplates for a nil TemplateProvider, email.ErrNilLogger
// for a nil logger, and email.ErrNilConfig for WithConfig(nil); a nil
// ConfigProvider is accepted, and Init then requires WithConfig.
func New(
	opts ...Option,
) func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*Service, error) {
	return func(
		templates gas.TemplateProvider,
		cfgProvider gas.ConfigProvider,
		logger gas.Logger,
	) (*Service, error) {
		if templates == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, email.ErrNilTemplates)
		}

		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, email.ErrNilLogger)
		}

		s := &Service{
			templates:   templates,
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}

		if s.cfgSource.IsNil() {
			return nil, fmt.Errorf("%s: %w", ServiceName, email.ErrNilConfig)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves the configuration and opens the sender, as described in
// the package documentation. On an initialized sender it returns
// email.ErrAlreadyInitialized and changes nothing.
func (s *Service) Init(ctx context.Context) error {
	var set Settings

	err := s.lc.Init(ctx, func() (Settings, error) {
		cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, rules)
		if err != nil {
			return Settings{}, err //nolint:wrapcheck // Init adds the name and the operation.
		}

		set = cfg.Email

		// open returns no error past this point, so lc.Init succeeds,
		// and its lock orders the Store with other Inits.
		s.bound.Store(cfg)

		return set, nil
	})
	if err != nil {
		return opError(opInit, err)
	}

	s.logger.Info("service initialized").
		Bool("from", set.FromEmail != "").
		Int("max_messages", set.MaxMessages).
		Int("max_bytes", set.MaxBytes).
		Send()

	return nil
}

// Close makes every later call but Init return an error wrapping
// email.ErrClosed, and waits for every send in flight to return, as
// described in the package documentation. When ctx ends first, it
// cancels the sends in flight, which return email.ErrClosed, and returns
// an error wrapping ctx's error. The outbox stays. Close on a sender
// that is not initialized returns nil and logs nothing.
func (s *Service) Close(ctx context.Context) error {
	sess, err := s.lc.Close(ctx)
	if sess == nil {
		return nil
	}

	if err != nil {
		err = opError(opClose, err)
		s.logger.Warn("service closed, sends in flight canceled").Err("error", err).Send()

		return err
	}

	s.logger.Info("service closed").Send()

	return nil
}

// Config returns a copy of the configuration the last successful Init
// resolved, defaults included, or the zero Config before one. A failed
// Init and Close leave it as it was. Config holds no slice, map or
// pointer, so the copy shares nothing with the sender.
func (s *Service) Config() Config {
	cfg := s.bound.Load()
	if cfg == nil {
		return Config{}
	}

	return *cfg
}

// CheckReady reports readiness without I/O: an error wrapping
// email.ErrNotInitialized or email.ErrClosed when the sender is not
// initialized, the context's error when ctx has ended, and else nil.
func (s *Service) CheckReady(ctx context.Context) error {
	sess, err := s.lc.Begin(ctx)
	if err != nil {
		return opError(opReady, err)
	}

	sess.End()

	return nil
}

// Send checks msg as email.CheckEmail does and stores a normalized copy
// of it in the outbox, as described in the package documentation.
func (s *Service) Send(ctx context.Context, msg *gas.Email) error {
	if msg == nil {
		return opError(opSend, email.ErrNilMessage)
	}

	sess, err := s.lc.Begin(ctx)
	if err != nil {
		return opError(opSend, err)
	}
	defer sess.End()

	return s.store(opSend, sess, msg)
}

// SendFromTemplate renders msg into a copy, as the email package
// describes under Templates, and sends the copy as Send does. It never
// changes msg.
func (s *Service) SendFromTemplate(ctx context.Context, msg *gas.TemplatedEmail) error {
	if msg == nil {
		return opError(opSendTemplate, email.ErrNilMessage)
	}

	sess, err := s.lc.Begin(ctx)
	if err != nil {
		return opError(opSendTemplate, err)
	}
	defer sess.End()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	defer stop()

	rendered, err := render.Render(rctx, s.templates, msg)

	switch {
	case err == nil:
	case ctx.Err() != nil:
		return opError(opSendTemplate, ctx.Err())
	case sess.Context().Err() != nil:
		return opError(opSendTemplate, email.ErrClosed)
	default:
		return opError(opSendTemplate, err)
	}

	return s.store(opSendTemplate, sess, rendered)
}

// Messages returns copies of the messages in the outbox, in the order they
// were sent, as normalized: changing what it returns changes no message
// of the outbox. It works in any state of the sender.
func (s *Service) Messages() []gas.Email {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]gas.Email, 0, len(s.outbox))

	for _, m := range s.outbox {
		out = append(out, *render.Copy(m))
	}

	return out
}

// Reset empties the outbox. It works in any state of the sender.
func (s *Service) Reset() {
	s.mu.Lock()

	s.outbox = nil
	s.outboxBytes = 0
	s.mu.Unlock()
}

// store checks msg with the settings of sess and stores a normalized
// copy, unless a Close canceled sess first.
func (s *Service) store(op string, sess *lifecycle.Session[Settings], msg *gas.Email) error {
	set := sess.Value

	out, err := email.CheckEmail(msg, set.FromEmail)
	if err != nil {
		return opError(op, err)
	}

	size := email.MessageSize(out)

	s.mu.Lock()
	defer s.mu.Unlock()

	// A Close whose context ended canceled the send; one that did not
	// wait on its context, such as a TemplateProvider that ignores it,
	// stores nothing.
	if sess.Context().Err() != nil {
		return opError(op, email.ErrClosed)
	}

	if len(s.outbox) >= set.MaxMessages || s.outboxBytes+size > set.MaxBytes {
		return opError(op, ErrOutboxFull)
	}

	s.outbox = append(s.outbox, out)

	s.outboxBytes += size

	return nil
}

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
}
