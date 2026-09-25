package dir

import (
	"time"
)

// Presigner generates presigned download and upload URLs for objects.
type Presigner interface {
	PresignGetObject(bucket, key string, ttl time.Duration) (string, error)
	PresignPutObject(bucket, key string, ttl time.Duration) (string, error)
}
