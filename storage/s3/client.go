package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newClient builds an S3 client from cfg: the configured region, static
// credentials when an access key ID is set, and a custom endpoint with
// path-style addressing when one is set. Anything not configured comes from
// the AWS SDK's default configuration. It sends no request.
func newClient(cfg *Config) (*s3.Client, error) {
	var opts []func(*config.LoadOptions) error

	if len(cfg.Storage.Region) > 0 {
		opts = append(opts, config.WithRegion(cfg.Storage.Region))
	}

	if len(cfg.Storage.AccessKeyID) > 0 {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.Storage.AccessKeyID,
				cfg.Storage.SecretAccessKey,
				cfg.Storage.SessionToken,
			),
		))
	}

	awsCfg, cfgErr := config.LoadDefaultConfig(context.Background(), opts...)
	if cfgErr != nil {
		return nil, fmt.Errorf("load AWS config: %w", cfgErr)
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if len(cfg.Storage.Endpoint) > 0 {
			o.BaseEndpoint = new(cfg.Storage.Endpoint)
			o.UsePathStyle = true
		}
	}), nil
}
