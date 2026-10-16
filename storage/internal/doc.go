// Package internal holds the helpers the storage backends share: bucket name
// and object key validation, input validation that resolves the bucket an
// operation uses, configuration binding, and error formatting.
//
// It is internal to the storage module so that the s3 and dir backends apply
// identical rules without exporting them to apps. It registers nothing with
// the DI container and holds no state.
//
// # Invariants
//
// IsValidBucketName implements S3's general purpose bucket naming rules, so a
// bucket name accepted by one backend is accepted by all. IsValidObjectKey
// is the minimum every backend enforces; a backend may reject more keys. It
// treats keys as slash-separated paths with package path, never
// path/filepath, so its result does not depend on GOOS.
// ValidateInput checks the key before the bucket and never falls back from
// an explicit bucket to the configured one, even when the explicit one is
// empty or invalid. Its errors are the bare storage sentinels.
//
// Errorf formats backend errors as "gas/storage/<mod>: <message>", for
// example "gas/storage/s3/provider: get: <cause>". Callers pass the operation
// and the cause, and must not pass a caller-supplied key or metadata value.
//
// SPDX-License-Identifier: MIT
package internal
