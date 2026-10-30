package secretsmanager

import (
	"context"
	"errors"
	"sync"
	"time"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gasmod/gas/config"
)

var (
	// ErrNoSecretsConfigured indicates that no secret was registered.
	ErrNoSecretsConfigured = errors.New("no secrets configured")
	// ErrInvalidKey indicates that a key passed to WithSecretAtKey has an
	// empty level.
	ErrInvalidKey = errors.New("invalid key")
	// ErrClientInitFailed indicates that the AWS configuration could not be
	// loaded to build the client.
	ErrClientInitFailed = errors.New("failed to initialize SecretsManager client")
	// ErrSecretFetchFailed indicates that GetSecretValue failed for a secret.
	ErrSecretFetchFailed = errors.New("failed to fetch secret")
	// ErrSecretDecodeFailed indicates that a secret's value could not be
	// turned into configuration values.
	ErrSecretDecodeFailed = errors.New("failed to decode secret")
)

const (
	// ProviderName is the name reported by Name.
	ProviderName = "AWS SecretsManager"

	// DefaultTimeout bounds each load unless WithTimeout says otherwise.
	DefaultTimeout = 10 * time.Second
)

// API is the part of the AWS Secrets Manager client that Provider uses.
// *secretsmanager.Client implements it.
type API interface {
	GetSecretValue(
		ctx context.Context,
		params *awssm.GetSecretValueInput,
		optFns ...func(*awssm.Options),
	) (*awssm.GetSecretValueOutput, error)
}

// secret is one registered secret. An empty key places it at the root.
type secret struct {
	name string
	key  string
}

// Provider loads configuration from AWS Secrets Manager, as described in
// the package documentation.
type Provider struct {
	// mu guards client, which WithClient sets or the first load builds.
	client          API
	region          string
	endpoint        string
	accessKeyID     string
	secretAccessKey string
	secrets         []secret
	timeout         time.Duration
	mu              sync.Mutex
}

var (
	_ config.Provider        = (*Provider)(nil)
	_ config.ContextProvider = (*Provider)(nil)
)

// Option configures a Provider.
type Option func(*Provider)

// WithSecret registers a secret, by name or ARN, whose value must be a JSON
// object. The object is merged at the root.
func WithSecret(name string) Option {
	return func(p *Provider) {
		p.secrets = append(p.secrets, secret{name: name})
	}
}

// WithSecretAtKey registers a secret, by name or ARN, placed at the
// dot-separated key. A JSON object is nested there, and any other value is
// stored there as its text. An empty key is the root, as for WithSecret,
// and a key with an empty level fails the load with ErrInvalidKey.
func WithSecretAtKey(name, key string) Option {
	return func(p *Provider) {
		p.secrets = append(p.secrets, secret{name: name, key: key})
	}
}

// WithRegion sets the AWS region. An empty region leaves it to the default
// AWS configuration.
func WithRegion(region string) Option {
	return func(p *Provider) {
		p.region = region
	}
}

// WithStaticCredentials sets static AWS credentials. Unless both are set,
// the default AWS credential chain is used.
func WithStaticCredentials(accessKeyID, secretAccessKey string) Option {
	return func(p *Provider) {
		p.accessKeyID = accessKeyID
		p.secretAccessKey = secretAccessKey
	}
}

// WithEndpoint sets the endpoint the client sends requests to, such as
// LocalStack's. An empty endpoint leaves it to the default AWS
// configuration.
func WithEndpoint(endpoint string) Option {
	return func(p *Provider) {
		p.endpoint = endpoint
	}
}

// WithClient makes the provider use client instead of building one, so
// WithRegion, WithStaticCredentials and WithEndpoint have no effect. A nil
// client builds one as without this option.
func WithClient(client API) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// WithTimeout sets how long a load may take, through Load or LoadContext.
// Zero or a negative duration means no limit of the provider's own.
//
// Default: DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) {
		p.timeout = d
	}
}

// NewProvider returns a Provider configured by opts.
func NewProvider(opts ...Option) *Provider {
	p := &Provider{timeout: DefaultTimeout}
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Name returns ProviderName.
func (p *Provider) Name() string {
	return ProviderName
}

// Load fetches the registered secrets, bounded by the provider's timeout.
func (p *Provider) Load() (map[string]any, error) {
	return p.LoadContext(context.Background())
}

// LoadContext fetches the registered secrets under ctx, bounded by the
// provider's timeout as well.
func (p *Provider) LoadContext(ctx context.Context) (map[string]any, error) {
	if len(p.secrets) == 0 {
		return nil, ErrNoSecretsConfigured
	}

	for _, s := range p.secrets {
		err := checkKey(s)
		if err != nil {
			return nil, err
		}
	}

	if p.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, p.timeout)

		defer cancel()
	}

	return p.load(ctx)
}
