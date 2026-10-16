package s3

// Config is the configuration NewProvider and NewPresigner bind from
// gas.ConfigProvider. See the package documentation for each key.
type Config struct {
	// Storage holds the keys under "storage".
	Storage struct {
		// Region is the AWS region. Empty uses the SDK's default resolution.
		Region string
		// Bucket is the bucket used when an input leaves Bucket nil.
		Bucket string
		// AccessKeyID enables static credentials when set.
		AccessKeyID string
		// SecretAccessKey is the secret for AccessKeyID.
		SecretAccessKey string
		// SessionToken is the optional session token for AccessKeyID.
		SessionToken string
		// Endpoint is a custom endpoint; setting it enables path-style
		// addressing.
		Endpoint string
	}
}
