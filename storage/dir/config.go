package dir

// Config is the configuration NewProvider binds from the gas.ConfigProvider.
type Config struct {
	// Storage holds the storage settings.
	Storage struct {
		// Bucket is the default bucket, used when an input's Bucket is nil
		// (config key storage.bucket). Empty means no default: every input
		// must name its bucket.
		Bucket string
	}
}
