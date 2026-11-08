package s3_test

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/internal/conformance"
	s3store "github.com/gasmod/gas/storage/s3"
)

// skipsS3 are the rules every S3 subject skips, with the reasons.
var skipsS3 = map[string]string{
	"Unsupported": "S3 keeps versions and parts, so a Version or a PartNumber " +
		"is sent, not refused",
	"HeadMissingBucket": "a HEAD reply has no body, so S3's 404 does not tell a " +
		"missing bucket from a missing key",
}

// subject returns the conformance subject of the S3 store and presigner
// whose configurations config returns.
func subject(
	config func(bucket string) *s3store.Config,
	skip map[string]string,
) conformance.Subject {
	return conformance.Subject{
		Name:          s3store.ServiceName,
		PresignerName: s3store.PresignerName,
		Skip:          skip,
		New: func(t *testing.T, bucket string) conformance.Store {
			t.Helper()

			p, err := s3store.New(s3store.WithConfig(config(bucket)))(nil, &gas.NopLogger{})
			require.NoError(t, err)

			return p
		},
		NewPresigner: func(t *testing.T, bucket string) conformance.Signer {
			t.Helper()

			p, err := s3store.NewPresigner(
				s3store.WithPresignerConfig(config(bucket)),
			)(nil, &gas.NopLogger{})
			require.NoError(t, err)

			return p
		},
	}
}

// The store and the presigner keep the contract of the storage package
// on the fake, which checks presigned signatures as S3 does.
func TestConformance_Fake(t *testing.T) {
	t.Parallel()

	f := newFake(t)

	conformance.Run(t, subject(f.config, skipsS3))
}

// The store and the presigner keep the contract of the storage package
// on LocalStack, which checks the signatures of presigned requests.
func TestConformance_LocalStack(t *testing.T) {
	t.Parallel()

	endpoint := localStackEndpoint(t)

	skip := maps.Clone(skipsS3)

	skip["ListNonASCII"] = "LocalStack 4.14 ends a page listed with encoding-type=url " +
		"and a continuation token before a key that is not ASCII, which S3 lists"
	skip["CopyKeepsExpires"] = "LocalStack 4.14 drops Expires from a copy with the COPY " +
		"metadata directive, which S3 documents as keeping all metadata but the " +
		"website redirect location"
	skip["CopyConcurrent"] = "LocalStack 4.14 at times stalls CopyObjects that cross, a to b " +
		"and b to a, at once, until they time out, as it does those the AWS SDK sends " +
		"without the store"

	conformance.Run(t, subject(func(bucket string) *s3store.Config {
		return localStackConfig(endpoint, bucket)
	}, skip))
}
