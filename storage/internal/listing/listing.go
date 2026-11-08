package listing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"iter"
	"strings"

	"github.com/gasmod/gas/storage"
)

// keySize is the size of a Codec's AES key: 32 bytes, AES-256.
const keySize = 32

// MaxCursorLength is the longest Cursor.After that Seal seals: 1536
// bytes, above storage.MaxKeyLength and the longest bucket name, so that
// it bounds only a token another server issued, such as S3's own.
const MaxCursorLength = 1536

// The parts of a sealed token around its cursor: the AEAD's random nonce
// and its tag, and the cursor's flag byte.
const (
	// nonceSize is the size of the nonce the AEAD prefixes.
	nonceSize = 12
	// tagSize is the size of the AEAD's tag.
	tagSize = 16
	// flagSize is the size of the cursor's Prefix flag.
	flagSize = 1
)

// maxTokenLength bounds the tokens Open decodes: the base64 of the longest
// token Seal makes, of a cursor of MaxCursorLength bytes, so that Open
// opens every token Seal makes and refuses any longer before decoding it.
var maxTokenLength = base64.RawURLEncoding.EncodedLen(
	nonceSize + flagSize + MaxCursorLength + tagSize,
)

// errForeignToken reports a token the Codec did not seal for the scope.
var errForeignToken = fmt.Errorf("%w: not one this backend issued for this listing",
	storage.ErrInvalidToken)

// ErrCursorTooLong reports a cursor longer than MaxCursorLength, which Seal
// refuses rather than make a token Open would not open.
var ErrCursorTooLong = fmt.Errorf("cursor longer than %d bytes", MaxCursorLength)

// Codec seals cursors into continuation tokens and opens them, as
// described in the package documentation. It is safe for concurrent use.
type Codec struct {
	// aead seals with a random nonce of its own.
	aead cipher.AEAD
}

// Cursor is where a page ended: the last entry it returned.
type Cursor struct {
	// After is the name of the entry.
	After string
	// Prefix reports whether the entry is a common prefix, so that the
	// next page passes over the keys it rolled up.
	Prefix bool
}

// NewCodec returns a Codec with a new random key.
func NewCodec() (*Codec, error) {
	key := make([]byte, keySize)
	// It never fails: it crashes the program instead.
	_, _ = rand.Read(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}

	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}

	return &Codec{aead: aead}, nil
}

// Scope returns the scope of a listing of the given parts, such as the
// bucket, the prefix, the delimiter and the StartAfter, so that a token
// opens only for the listing it was sealed for. Each part is prefixed with
// its length, so that no two lists of parts share a scope.
func Scope(parts ...string) []byte {
	var out []byte

	for _, p := range parts {
		out = binary.AppendUvarint(out, uint64(len(p)))
		out = append(out, p...)
	}

	return out
}

// Seal returns the token of c for the listing of scope: unpadded base64url
// of c encrypted and authenticated with the Codec's key, with scope as
// additional data. The token tells nothing of c to one without the key.
// A cursor longer than MaxCursorLength is ErrCursorTooLong, which never
// quotes it.
func (c *Codec) Seal(scope []byte, cur Cursor) (string, error) {
	if len(cur.After) > MaxCursorLength {
		return "", ErrCursorTooLong
	}

	plain := make([]byte, 0, flagSize+len(cur.After))
	if cur.Prefix {
		plain = append(plain, 1)
	} else {
		plain = append(plain, 0)
	}

	plain = append(plain, cur.After...)

	// The nonce is empty: the AEAD draws a random one and prefixes it.
	//nolint:gosec // G407: the nonce is random, as above.
	sealed := c.aead.Seal(nil, nil, plain, scope)

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open returns the Cursor token holds, when the Codec sealed it for the
// listing of scope, and else an error wrapping storage.ErrInvalidToken
// that never quotes the token.
func (c *Codec) Open(scope []byte, token string) (Cursor, error) {
	if len(token) > maxTokenLength {
		return Cursor{}, errForeignToken
	}

	sealed, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return Cursor{}, errForeignToken
	}

	plain, err := c.aead.Open(nil, nil, sealed, scope)
	if err != nil || len(plain) < 2 || plain[0] > 1 {
		return Cursor{}, errForeignToken
	}

	return Cursor{After: string(plain[1:]), Prefix: plain[0] == 1}, nil
}

// Entry is one entry of a page: a key, or a common prefix.
type Entry struct {
	// Name is the key or the common prefix.
	Name string
	// Prefix reports whether Name is a common prefix.
	Prefix bool
}

// Query is what a page lists, as the package documentation describes.
type Query struct {
	// Prefix is the prefix every key listed starts with.
	Prefix string
	// Delimiter rolls up keys into common prefixes, or is empty for none.
	Delimiter string
	// StartAfter passes over the keys up to it.
	StartAfter string
	// After is where the previous page ended, or the zero Cursor for the
	// first page.
	After Cursor
	// Max is the most entries of the page, at least 1.
	Max int
}

// Page returns the entries of the page q selects from keys, which are in
// ascending byte order without duplicates, as described in the package
// documentation, and whether another page follows. It stops reading keys
// at the first entry past the page.
func Page(keys iter.Seq[string], q Query) (entries []Entry, truncated bool) {
	var lastPrefix string

	for k := range keys {
		if !q.selects(k) {
			continue
		}

		e := q.entry(k)
		if e.Prefix && e.Name == lastPrefix {
			continue
		}

		if len(entries) == q.Max {
			return entries, true
		}

		entries = append(entries, e)

		if e.Prefix {
			lastPrefix = e.Name
		}
	}

	return entries, false
}

// selects reports whether the page may list k, or the common prefix it
// rolls up into: k starts with the prefix, comes after StartAfter and
// after the previous page.
func (q *Query) selects(k string) bool {
	switch {
	case !strings.HasPrefix(k, q.Prefix) || k <= q.StartAfter:
		return false
	case q.After.After == "":
		return true
	case k <= q.After.After:
		return false
	default:
		return !q.After.Prefix || !strings.HasPrefix(k, q.After.After)
	}
}

// entry returns the entry of k: the common prefix it rolls up into, or
// itself.
func (q *Query) entry(k string) Entry {
	if q.Delimiter != "" {
		i := strings.Index(k[len(q.Prefix):], q.Delimiter)
		if i >= 0 {
			return Entry{Name: k[:len(q.Prefix)+i+len(q.Delimiter)], Prefix: true}
		}
	}

	return Entry{Name: k}
}
