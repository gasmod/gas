// Package secretsmanager provides a gas/config provider that loads
// configuration from AWS Secrets Manager. Secrets are registered explicitly
// and fetched eagerly when the configuration is loaded.
package secretsmanager

import (
	"context"
	"errors"
	"time"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gasmod/gas/config/providers"
)

var (
	// ErrNoSecretsConfigured indicates that Load was called without any registered secrets.
	ErrNoSecretsConfigured = errors.New("no secrets configured")
	// ErrSecretFetchFailed indicates a failure to fetch a secret from AWS SecretsManager.
	ErrSecretFetchFailed = errors.New("failed to fetch secret")
	// ErrSecretDecodeFailed indicates that a secret value could not be decoded.
	ErrSecretDecodeFailed = errors.New("failed to decode secret")
	// ErrClientInitFailed indicates a failure to initialize the SecretsManager client.
	ErrClientInitFailed = errors.New("failed to initialize SecretsManager client")
)

const (
	// ProviderName is the name reported by Name.
	ProviderName = "AWS SecretsManager"

	// DefaultTimeout bounds Load when no caller context is supplied.
	DefaultTimeout = 10 * time.Second
)

// API is the subset of the AWS SecretsManager client used by the provider.
// *secretsmanager.Client satisfies it; tests may inject a mock via WithClient.
type API interface {
	GetSecretValue(ctx context.Context, params *awssm.GetSecretValueInput, optFns ...func(*awssm.Options)) (*awssm.GetSecretValueOutput, error)
}

// secretRef is one registered secret. An empty key merges the secret's JSON
// object at the root; a non-empty key nests the value at that dot-path.
type secretRef struct {
	name string
	key  string
}

// Provider loads configuration from AWS Secrets Manager.
type Provider struct {
	region          string
	endpoint        string
	accessKeyID     string
	secretAccessKey string
	client          API
	secrets         []secretRef
	timeout         time.Duration
}

var (
	_ providers.Provider        = (*Provider)(nil)
	_ providers.ContextProvider = (*Provider)(nil)
)

// Option is a function that configures a Provider.
type Option func(*Provider)

// WithSecret registers a secret (name or full ARN) whose value must decode to
// a JSON object. The object is deep-merged into the configuration at the
// root. Repeatable; later registrations win on key conflicts.
func WithSecret(name string) Option {
	return func(p *Provider) {
		p.secrets = append(p.secrets, secretRef{name: name})
	}
}

// WithSecretAtKey registers a secret placed at the dot-notation key. A JSON
// object value is nested at that path; any other value is placed at the path
// as a raw string. An empty key behaves like WithSecret.
func WithSecretAtKey(name, key string) Option {
	return func(p *Provider) {
		p.secrets = append(p.secrets, secretRef{name: name, key: key})
	}
}

// WithRegion sets the AWS region.
func WithRegion(region string) Option {
	return func(p *Provider) {
		p.region = region
	}
}

// WithStaticCredentials sets static AWS credentials. When either value is
// empty, the default AWS credential chain is used instead.
func WithStaticCredentials(accessKeyID, secretAccessKey string) Option {
	return func(p *Provider) {
		p.accessKeyID = accessKeyID
		p.secretAccessKey = secretAccessKey
	}
}

// WithEndpoint sets a custom endpoint (e.g. LocalStack).
func WithEndpoint(endpoint string) Option {
	return func(p *Provider) {
		p.endpoint = endpoint
	}
}

// WithClient injects a pre-built client. When set, region, credential, and
// endpoint options are ignored.
func WithClient(client API) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// WithTimeout bounds Load when no caller context is supplied.
//
// Default: DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) {
		p.timeout = d
	}
}

// NewProvider creates a new AWS SecretsManager provider.
func NewProvider(opts ...Option) *Provider {
	pvd := &Provider{
		timeout: DefaultTimeout,
	}

	for _, opt := range opts {
		opt(pvd)
	}

	return pvd
}

// Name implements the Provider interface.
func (p *Provider) Name() string {
	return ProviderName
}

// Load implements the Provider interface using a background context bounded
// by the configured timeout.
func (p *Provider) Load() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	return p.LoadContext(ctx)
}

// LoadContext implements the providers.ContextProvider interface.
func (p *Provider) LoadContext(ctx context.Context) (map[string]any, error) {
	if len(p.secrets) == 0 {
		return nil, ErrNoSecretsConfigured
	}

	return p.load(ctx)
}
