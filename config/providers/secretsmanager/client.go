package secretsmanager

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// getClient returns the injected client or builds one from the configured
// region, credentials, and endpoint, caching it for subsequent loads.
func (p *Provider) getClient(ctx context.Context) (API, error) {
	if p.client != nil {
		return p.client, nil
	}

	opts := []func(*awsconfig.LoadOptions) error{}

	if p.region != "" {
		opts = append(opts, awsconfig.WithRegion(p.region))
	}

	if p.accessKeyID != "" && p.secretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(p.accessKeyID, p.secretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	p.client = awssm.NewFromConfig(awsCfg, func(o *awssm.Options) {
		if p.endpoint != "" {
			o.BaseEndpoint = new(p.endpoint)
		}
	})

	return p.client, nil
}
