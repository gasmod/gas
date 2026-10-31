// Package listing holds what the backends of the module that list from
// their own store share: the pages of ListObjects and ListBuckets, and
// their continuation tokens.
//
// # Usage
//
// A backend keeps a [Codec] per Init, and builds each page from its keys
// in byte order:
//
//	codec, err := listing.NewCodec()
//
//	scope := listing.Scope(req.Bucket, req.Prefix, req.Delimiter, req.StartAfter)
//	q := listing.Query{Prefix: req.Prefix, Delimiter: req.Delimiter,
//		StartAfter: req.StartAfter, Max: req.MaxKeys}
//	if req.ContinuationToken != "" {
//		q.After, err = codec.Open(scope, req.ContinuationToken)
//	}
//	entries, truncated := listing.Page(slices.Values(keys), q)
//	if truncated {
//		last := entries[len(entries)-1]
//		next, err := codec.Seal(scope, listing.Cursor{After: last.Name, Prefix: last.Prefix})
//	}
//
// # Pages
//
// [Page] lists the keys that start with the prefix and come after
// StartAfter and after the previous page's last entry, in byte order. A
// key that holds the delimiter after the prefix is rolled up into the
// common prefix that ends with the first such delimiter, and each common
// prefix is one entry, in the place of its first key. When the previous
// page ended with a common prefix, the keys it rolled up are passed over.
// A page holds at most Max entries, and is truncated when another entry
// follows.
//
// # Tokens
//
// A token seals the last entry of a page, a key or a common prefix, with
// AES-256-GCM under a key a [Codec] draws at random, with the scope of
// the listing as additional data: the bucket, prefix, delimiter and
// StartAfter it was issued for. A token tells nothing of the key it holds,
// and [Codec.Open] refuses one that was changed, one sealed by another
// Codec, such as before a restart, and one sealed for another listing.
// A token is good only for the Codec that sealed it: it does not survive
// a restart and does not move between processes.
//
// [Codec.Seal] seals a cursor of at most [MaxCursorLength] bytes (1536),
// and refuses a longer one with [ErrCursorTooLong]; [Codec.Open] refuses a
// token longer than the longest Seal makes before decoding it, so it
// opens every token Seal makes.
//
// # Errors
//
// Open's error wraps storage.ErrInvalidToken and never quotes the token.
// Seal's error is ErrCursorTooLong and never quotes the cursor:
//
//	invalid continuation token: not one this backend issued for this listing
//	cursor longer than 1536 bytes
package listing
