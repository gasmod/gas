package ses

import (
	"bytes"
	"context"
	"fmt"
	htmltemplate "html/template"
	"io"
	"sync/atomic"
	texttemplate "text/template"

	"github.com/gasmod/gas"
	email "github.com/gasmod/gas/email"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awsses "github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

const serviceName = "gas/email/ses"

// sesClient is the subset of the AWS SES client API used by this service.
// The real *ses.Client satisfies this interface.
type sesClient interface {
	SendEmail(ctx context.Context, params *awsses.SendEmailInput, optFns ...func(*awsses.Options)) (*awsses.SendEmailOutput, error)
}

// Service is an SES-backed email sender implementing gas.Service and
// gas.EmailProvider.
type Service struct {
	client    sesClient
	templates gas.TemplateProvider
	cfg       *Config
	logger    gas.Logger

	cfgProvider          gas.ConfigProvider
	customConfigProvided bool
	customClientProvided bool
	closed               atomic.Bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.EmailProvider = (*Service)(nil)
var _ gas.ReadyReporter = (*Service)(nil)

// Client returns the underlying *ses.Client for advanced operations
// beyond the EmailProvider interface. Returns nil if a custom sesClient
// was injected via WithClient that is not an *ses.Client.
func (s *Service) Client() *awsses.Client {
	c, _ := s.client.(*awsses.Client)
	return c
}

// Option configures a Service.
type Option func(*Service)

// WithConfig sets a custom configuration.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// WithClient injects a pre-configured SES client. Useful for testing
// or when the caller manages AWS credentials externally.
func WithClient(client sesClient) Option {
	return func(s *Service) {
		s.client = client
		s.customClientProvided = true
	}
}

// New captures options and returns a DI-injectable constructor.
func New(opts ...Option) func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) *Service {
	return func(templates gas.TemplateProvider, cfgProvider gas.ConfigProvider, logger gas.Logger) *Service {
		s := &Service{
			cfg:         DefaultConfig(),
			templates:   templates,
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", serviceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// NewWithCustomProvider creates a new Service instance with a custom TemplateProvider and optional configurations.
func NewWithCustomProvider[T gas.TemplateProvider](opts ...Option) func(T, gas.ConfigProvider, gas.Logger) *Service {
	return func(templates T, cfgProvider gas.ConfigProvider, logger gas.Logger) *Service {
		s := &Service{
			cfg:         DefaultConfig(),
			templates:   templates,
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", serviceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// Init validates the configuration and creates the SES client.
func (s *Service) Init() error {
	if !s.customConfigProvided {
		if s.cfgProvider != nil {
			if err := s.cfgProvider.Bind(s.cfg); err != nil {
				return fmt.Errorf("%s: config binding: %w", s.Name(), err)
			}
		}
	}

	if err := s.cfg.Validate(); err != nil {
		s.logger.Error("invalid email configuration").Err("error", err).Send()
		return err
	}

	if !s.customClientProvided {
		if err := s.createClient(); err != nil {
			return err
		}
	}

	s.closed.Store(false)
	s.logger.Info("ses email service initialized").Str("region", s.cfg.Email.Region).Send()
	return nil
}

func (s *Service) createClient() error {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(s.cfg.Email.Region),
	}

	if s.cfg.Email.AccessKeyID != "" && s.cfg.Email.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				s.cfg.Email.AccessKeyID,
				s.cfg.Email.SecretAccessKey,
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return fmt.Errorf("%s: load aws config: %w", s.Name(), err)
	}

	var sesOpts []func(*awsses.Options)
	if s.cfg.Email.Endpoint != "" {
		sesOpts = append(sesOpts, func(o *awsses.Options) {
			o.BaseEndpoint = new(s.cfg.Email.Endpoint)
		})
	}

	s.client = awsses.NewFromConfig(awsCfg, sesOpts...)
	return nil
}

// CheckReady reports whether the service can accept Send calls. Returns
// email.ErrClosed once Close has been invoked so probes depool the pod
// during shutdown/drain.
func (s *Service) CheckReady(_ context.Context) error {
	if s.closed.Load() {
		return email.ErrClosed
	}
	return nil
}

// Close marks the service as closed.
func (s *Service) Close() error {
	s.closed.Store(true)
	s.logger.Info("ses email service closed").Send()
	return nil
}

// Send sends an email using AWS SES.
func (s *Service) Send(ctx context.Context, msg *gas.Email) error {
	if s.closed.Load() {
		return email.ErrClosed
	}

	from := msg.From
	if from == "" {
		from = s.cfg.Email.FromEmail
	}

	var replyTo []string
	if msg.ReplyTo != "" {
		replyTo = append(replyTo, msg.ReplyTo)
	}

	_, err := s.client.SendEmail(ctx, &awsses.SendEmailInput{
		Destination: &types.Destination{
			ToAddresses:  msg.To,
			CcAddresses:  msg.Cc,
			BccAddresses: msg.Bcc,
		},
		Message: &types.Message{
			Subject: &types.Content{
				Data:    new(msg.Subject),
				Charset: new("UTF-8"),
			},
			Body: &types.Body{
				Text: &types.Content{
					Data:    new(msg.TextBody),
					Charset: new("UTF-8"),
				},
				Html: &types.Content{
					Data:    new(msg.HTMLBody),
					Charset: new("UTF-8"),
				},
			},
		},
		Source:           new(from),
		ReplyToAddresses: replyTo,
	})
	if err != nil {
		s.logger.Error("failed to send email").Err("error", err).Send()
		return fmt.Errorf("%s: failed to send email: %w", s.Name(), err)
	}

	return nil
}

// SendFromTemplate renders email templates and sends the resulting email
// using AWS SES. It fetches raw template content from the TemplateProvider,
// parses it, and executes it with the provided data.
func (s *Service) SendFromTemplate(ctx context.Context, msg *gas.TemplatedEmail) error {
	if s.closed.Load() {
		return email.ErrClosed
	}

	if msg.SubjectTemplate != "" {
		rendered, err := s.renderText(ctx, msg.SubjectTemplate, msg.Data)
		if err != nil {
			return fmt.Errorf("%s: render subject template %q: %w", s.Name(), msg.SubjectTemplate, err)
		}
		msg.Subject = rendered
	}

	if msg.HTMLTemplate != "" {
		rendered, err := s.renderHTML(ctx, msg.HTMLTemplate, msg.Data)
		if err != nil {
			return fmt.Errorf("%s: render html template %q: %w", s.Name(), msg.HTMLTemplate, err)
		}
		msg.HTMLBody = rendered
	}

	if msg.TextTemplate != "" {
		rendered, err := s.renderText(ctx, msg.TextTemplate, msg.Data)
		if err != nil {
			return fmt.Errorf("%s: render text template %q: %w", s.Name(), msg.TextTemplate, err)
		}
		msg.TextBody = rendered
	}

	return s.Send(ctx, &msg.Email)
}

func (s *Service) renderHTML(ctx context.Context, name string, data any) (string, error) {
	return s.render(ctx, renderHTML, name, data)
}

func (s *Service) renderText(ctx context.Context, name string, data any) (string, error) {
	return s.render(ctx, renderText, name, data)
}

const (
	renderText = "text"
	renderHTML = "html"
)

type templateExecutor interface {
	Execute(wr io.Writer, data any) error
}

func (s *Service) render(ctx context.Context, engine, name string, data any) (string, error) {
	content, cErr := s.templates.Get(ctx, name)
	if cErr != nil {
		return "", fmt.Errorf("get template %q: %w", name, cErr)
	}

	var (
		tmplExec templateExecutor
		err      error
	)

	if engine == renderText {
		tmplExec, err = texttemplate.New(name).Parse(string(content))
	} else {
		tmplExec, err = htmltemplate.New(name).Parse(string(content))
	}

	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}

	var buf bytes.Buffer
	if err = tmplExec.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}

	return buf.String(), nil
}
