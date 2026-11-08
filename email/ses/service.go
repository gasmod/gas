package ses

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/configsrc"
	"github.com/gasmod/gas/email/internal/lifecycle"
)

// ServiceName is the name of the SES sender, returned by Service.Name and
// prefixed to every error the sender returns.
const ServiceName = "gas/email/ses"

// The operations errors name.
const (
	opInit         = "init"
	opReady        = "ready"
	opSend         = "send"
	opSendTemplate = "send from template"
	opClose        = "close"
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
	// ErrConnect indicates that a send got no reply from SES: it timed
	// out, its connection was refused, or the endpoint was unreachable, as
	// described in the package documentation.
	ErrConnect = errors.New("no reply")
	// ErrCommand indicates that SES replied to a send with an error, or
	// that a send failed for another reason than ErrConnect's, as
	// described in the package documentation.
	ErrCommand = errors.New("request failed")
	// ErrRejected indicates that SES refused the message, with
	// MessageRejected. It comes with ErrCommand.
	ErrRejected = errors.New("rejected")
	// ErrNotVerified indicates that SES has not verified the sender's
	// address or domain, or, in the sandbox, a recipient. It comes with
	// ErrCommand.
	ErrNotVerified = errors.New("not verified")
	// ErrThrottled indicates that SES throttled the send, past the
	// retries. It comes with ErrCommand.
	ErrThrottled = errors.New("throttled")
	// ErrSendingPaused indicates that the account may not send: its
	// sending is paused or suspended. It comes with ErrCommand.
	ErrSendingPaused = errors.New("sending paused")
	// ErrAccessDenied indicates that SES refused the credentials, or their
	// permission for the send. It comes with ErrCommand.
	ErrAccessDenied = errors.New("access denied")
)

// API is the part of the SES API v2 client the sender uses.
// *sesv2.Client implements it.
type API interface {
	// SendEmail sends a message.
	SendEmail(
		ctx context.Context,
		params *sesv2.SendEmailInput,
		optFns ...func(*sesv2.Options),
	) (*sesv2.SendEmailOutput, error)
}

var _ API = (*sesv2.Client)(nil)

// Service is an email sender on Amazon SES, as described in the package
// documentation. It is safe for concurrent use.
type Service struct {
	// templates is the TemplateProvider SendFromTemplate fetches from.
	templates gas.TemplateProvider
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the sender's events, with `service` set.
	logger gas.Logger
	// client is the client given to WithClient.
	client API
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// lc is the lifecycle, whose sessions hold what Init opened.
	lc lifecycle.Lifecycle[*state]
	// clientSet reports whether WithClient was given, so that a nil
	// client is told apart from none.
	clientSet bool
}

var (
	_ gas.Service       = (*Service)(nil)
	_ gas.EmailProvider = (*Service)(nil)
	_ gas.ReadyReporter = (*Service)(nil)
)

// state is what one Init opened, with the settings sends use.
type state struct {
	// client sends the messages.
	client API
	// transport is the HTTP transport of a client Init built, or nil.
	transport *http.Transport
	// from is Settings.FromEmail, normalized.
	from string
	// opTimeout is Settings.OpTimeout.
	opTimeout time.Duration
}

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

// Init resolves and checks the configuration, builds the client and
// retrieves the credentials, as described in the package documentation.
// On an initialized sender it returns email.ErrAlreadyInitialized and
// changes nothing. A failed Init leaves the sender as it was.
func (s *Service) Init(ctx context.Context) error {
	var (
		set            *Settings
		region, source string
	)

	err := s.lc.Init(ctx, func() (*state, error) {
		cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, rules)
		if err != nil {
			return nil, err //nolint:wrapcheck // Init adds the name and the operation.
		}

		set = &cfg.Email

		st := &state{client: s.client, from: set.FromEmail, opTimeout: set.OpTimeout}

		switch {
		case s.clientSet && isNil(s.client):
			return nil, ErrNilClient
		case !s.clientSet:
			var built *sesv2.Client

			built, st.transport, source, err = s.build(ctx, set)
			if err != nil {
				return nil, err
			}

			st.client, region = built, built.Options().Region
		}

		// open returns no error past this point, so lc.Init succeeds,
		// and its lock orders the Store with other Inits.
		s.bound.Store(cfg)

		return st, nil
	})
	if err != nil {
		return opError(opInit, err)
	}

	s.logInitialized(set, region, source)

	return nil
}

// Close makes every later call but Init return an error wrapping
// email.ErrClosed, and waits for every send in flight to return, as
// described in the package documentation. When ctx ends first, it
// cancels the sends in flight, which return email.ErrClosed, and returns
// an error wrapping ctx's error; the sender stays closed. Close then
// closes the idle connections of a client Init built. Close on a sender
// that is not initialized returns nil and logs nothing.
func (s *Service) Close(ctx context.Context) error {
	sess, err := s.lc.Close(ctx)
	if sess == nil {
		return nil
	}

	if t := sess.Value.transport; t != nil {
		t.CloseIdleConnections()
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
// pointer, so the copy shares nothing with the sender, and SecretAccessKey and SessionToken stay Secrets.
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

// Client returns the *sesv2.Client the sender sends with, or nil when
// the sender is not initialized or was given another API with
// WithClient, as described in the package documentation.
func (s *Service) Client() *sesv2.Client {
	sess, err := s.lc.Begin(context.Background())
	if err != nil {
		return nil
	}
	defer sess.End()

	c, _ := sess.Value.client.(*sesv2.Client)

	return c
}

// logInitialized logs `service initialized`, as described in the package
// documentation, with the credential source unless a client was given.
func (s *Service) logInitialized(set *Settings, region, source string) {
	event := s.logger.Info("service initialized").
		Str("region", region).
		Bool("endpoint", set.Endpoint != "").
		Bool("client", s.clientSet).
		Bool("from", set.FromEmail != "")
	if !s.clientSet {
		event = event.Str("credentials", source)
	}

	event.Duration("op_timeout", set.OpTimeout).Int("max_retries", set.MaxRetries).Send()
}

// build returns the client buildClient builds of set, its transport,
// and where it takes its credentials from, as credentialSource tells,
// after it logs a warning naming the keys missing from a partial set of
// static credentials.
func (s *Service) build(
	ctx context.Context,
	set *Settings,
) (*sesv2.Client, *http.Transport, string, error) {
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

// isNil reports whether client is nil, or a nil *sesv2.Client.
func isNil(client API) bool {
	c, ok := client.(*sesv2.Client)

	return client == nil || ok && c == nil
}

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %w", opPrefix(op), err)
}

// opPrefix returns the start of the errors of op: ServiceName and the
// operation.
func opPrefix(op string) string {
	return ServiceName + ": " + op
}
