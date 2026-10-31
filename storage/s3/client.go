package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/logging"

	"github.com/gasmod/gas/storage"
)

// errAWSConfig reports AWS configuration that failed to load, such as a
// profile that does not exist. The AWS SDK's error is not kept: it can
// name files and profiles.
var errAWSConfig = errors.New("AWS configuration failed to load")

// maxReplySize is the largest response body the client Init builds
// reads, but for the contents GetObject returns, in bytes: 16 MiB, far
// above the largest page of ListObjects, 1000 keys of 1024 bytes each
// URL-encoded to 3072, or of ListBuckets. A larger one fails its request
// with the reason `invalid reply`, as described in the package
// documentation.
const maxReplySize = 16 << 20

// errReplyTooLarge reports a response body larger than maxReplySize.
var errReplyTooLarge = fmt.Errorf("%w: body larger than %d bytes", errInvalidReply, maxReplySize)

// opGetObjectName is the AWS SDK's name of the GetObject operation,
// whose successful response body limitReplies leaves whole.
const opGetObjectName = "GetObject"

// The credential sources Init logs, as described in the package
// documentation.
const (
	// sourceStatic is the static credentials of the settings.
	sourceStatic = "static"
	// sourceChain is the AWS SDK's default chain.
	sourceChain = "default chain"
)

// credentialSource returns where the client of set takes its
// credentials from: sourceStatic when both keys are set, else
// sourceChain, with the names of the keys that are missing when some of
// the static credentials are set but not both keys.
func credentialSource(set *Settings) (string, []string) {
	if set.AccessKeyID != "" && set.SecretAccessKey != "" {
		return sourceStatic, nil
	}

	if set.AccessKeyID == "" && set.SecretAccessKey == "" && set.SessionToken == "" {
		return sourceChain, nil
	}

	var missing []string

	if set.AccessKeyID == "" {
		missing = append(missing, "storage.accesskeyid")
	}

	if set.SecretAccessKey == "" {
		missing = append(missing, "storage.secretaccesskey")
	}

	return sourceChain, missing
}

// limitReplies is an http.RoundTripper whose response bodies fail with
// errReplyTooLarge past maxReplySize, so that no reply can make the
// store read without bound. The body of a successful GetObject, the
// object's contents, which the caller reads as a stream, is left whole.
type limitReplies struct {
	// next sends the requests.
	next http.RoundTripper
}

// RoundTrip sends req with next, and limits the body of its response but
// for a successful GetObject's.
func (l limitReplies) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := l.next.RoundTrip(req)
	if err != nil {
		//nolint:wrapcheck // the operation's requestError classifies it.
		return nil, err
	}

	isContents := awsmiddleware.GetOperationName(req.Context()) == opGetObjectName &&
		resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	if !isContents {
		resp.Body = &limitedBody{body: resp.Body, left: maxReplySize}
	}

	return resp, nil
}

// limitedBody is a response body that fails with errReplyTooLarge once
// more than its limit is read.
type limitedBody struct {
	// body is the response body.
	body io.ReadCloser
	// left is how many more bytes may be read.
	left int64
}

// Read reads from the body, up to one byte past the limit, which fails
// with errReplyTooLarge.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errReplyTooLarge
	}

	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}

	n, err := b.body.Read(p)

	b.left -= int64(n)
	if b.left < 0 {
		return n, errReplyTooLarge
	}

	//nolint:wrapcheck // io.EOF must stay as it is, and the SDK classifies the rest.
	return n, err
}

// Close closes the body.
func (b *limitedBody) Close() error {
	//nolint:wrapcheck // the SDK's to handle.
	return b.body.Close()
}

// built is a client buildClient built, with what Init keeps of it.
type built struct {
	// client is the S3 client.
	client *s3.Client
	// transport is the client's HTTP transport, whose idle connections
	// Close closes.
	transport *http.Transport
	// region is the region the client resolved.
	region string
}

// buildClient builds the S3 client of set, as described in the package
// documentation: the region and the static credentials when set, else
// the AWS SDK's default chain, then the endpoint, the addressing and the
// retries, which AWS_MAX_ATTEMPTS and AWS_RETRY_MODE do not change. Its
// HTTP client follows no redirect and limits each reply but a
// GetObject's contents to maxReplySize. It retrieves the credentials
// within ctx and set.OpTimeout, so that Init fails when there are none.
func buildClient(ctx context.Context, set *Settings) (*built, error) {
	// The AWS SDK's own transport, with its timeouts, in a client that
	// follows no redirect, as the SDK's does not, and limits each reply.
	transport := awshttp.NewBuildableClient().GetTransport()
	opts := []func(*awsconfig.LoadOptions) error{
		// The AWS SDK logs to standard error by default, outside the
		// store's logger, with text of its own that can hold what a
		// server sent, such as a header value; it logs nothing here.
		awsconfig.WithLogger(logging.Nop{}),
		awsconfig.WithHTTPClient(&http.Client{
			Transport: limitReplies{next: transport},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}),
	}

	if set.Region != "" {
		opts = append(opts, awsconfig.WithRegion(set.Region))
	}

	if source, _ := credentialSource(set); source == sourceStatic {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				set.AccessKeyID,
				string(set.SecretAccessKey),
				string(set.SessionToken),
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", storage.ErrInvalidConfig, errAWSConfig)
	}

	if awsCfg.Region == "" {
		return nil, ErrNoRegion
	}

	credCtx, cancel := context.WithTimeout(ctx, set.OpTimeout)
	defer cancel()

	// Loading the configuration does not fetch credentials; the SDK
	// fetches them on the first request. Fetching them here fails
	// startup when there are none, and the configuration's cache keeps
	// them, refreshing them as they expire.
	_, err = awsCfg.Credentials.Retrieve(credCtx)
	if err != nil {
		return nil, credentialsError(ctx, err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if set.Endpoint != "" {
			o.BaseEndpoint = new(set.Endpoint)
		}

		o.UsePathStyle = set.pathStyle()
		o.Retryer = retry.NewStandard(func(so *retry.StandardOptions) {
			so.MaxAttempts = set.MaxRetries + 1
		})
		// The client applies RetryMaxAttempts over the Retryer after
		// this option runs, and the AWS configuration sets it from
		// AWS_MAX_ATTEMPTS, so it is set too, for MaxRetries to hold.
		o.RetryMaxAttempts = set.MaxRetries + 1
		// The SDK warns on its logger, standard error by default, of
		// every response without a checksum it can validate, such as an
		// object uploaded in parts or written by a server that sends none.
		o.DisableLogOutputChecksumValidationSkipped = true
	})

	return &built{client: client, transport: transport, region: awsCfg.Region}, nil
}
