package secretsmanager

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// getClient returns the client, building it from the options on the first
// call. A failed build is not kept, so the next call tries again.
func (p *Provider) getClient(ctx context.Context) (API, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return p.client, nil
	}

	var opts []func(*awsconfig.LoadOptions) error

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
		return nil, fmt.Errorf("%w: %w", ErrClientInitFailed, err)
	}

	p.client = awssm.NewFromConfig(awsCfg, func(o *awssm.Options) {
		if p.endpoint != "" {
			o.BaseEndpoint = new(p.endpoint)
		}
	})

	return p.client, nil
}
