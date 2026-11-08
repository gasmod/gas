// Package cryptoutil holds the cryptographic helpers the auth module's
// services share: random secrets, their hashes, and comparing secrets in
// constant time.
//
// # Usage
//
// A service makes a secret, gives it to the caller, and keeps only its
// hash:
//
//	secret, err := cryptoutil.RandomString(32)
//	if err != nil {
//		return err
//	}
//	stored := cryptoutil.SHA256Hex(secret)
//	// later, with the secret presented again:
//	ok := cryptoutil.Equal(cryptoutil.SHA256Hex(presented), stored)
//
// # Random strings
//
// [RandomString] reads n bytes from [crypto/rand] and encodes them as
// unpadded URL-safe base64, so the result is about 4n/3 characters long and
// safe in a URL, a header or a cookie without escaping. 32 bytes, 256
// bits, is the size the services use by default. n of 0 gives the empty
// string.
//
// # Hashes
//
// [SHA256Hex] returns the SHA-256 digest of a string as 64 lowercase hex
// characters. It is not salted, which suits random secrets of 128 bits or
// more: they cannot be guessed from their hash, and an unsalted hash can
// be looked up with an index.
//
// # Comparing
//
// [Equal] reports whether two strings are the same, taking time that
// depends on their lengths but not on their contents, so it does not tell
// an attacker how much of a guess was right. Two strings of different
// lengths are unequal at once, so compare values of a fixed length, such
// as two SHA256Hex digests.
//
// # Errors
//
// RandomString returns an error wrapping [ErrInvalidLength] for a negative
// n:
//
//	invalid random string length: must not be negative
//
// It also returns the error of crypto/rand, which documents that it never
// returns one.
package cryptoutil
