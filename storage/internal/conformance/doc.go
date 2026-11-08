// Package conformance holds the tests every backend of the module runs:
// the contract of the storage package, checked with identical
// expectations against a backend built by the caller.
//
// # Usage
//
// A backend's tests call [Run] with a [Subject] that builds a new,
// uninitialized store with the configured bucket the suite asks for:
//
//	conformance.Run(t, conformance.Subject{
//		Name: memory.ServiceName,
//		New: func(t *testing.T, bucket string) conformance.Store {
//			cfg := memory.DefaultConfig()
//			cfg.Storage.Bucket = bucket
//			p, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
//			require.NoError(t, err)
//			return p
//		},
//	})
//
// The stores of one Subject share one backend, such as one server: a
// rule creates the buckets it uses, under names no other rule or run
// uses, so a server may be shared by every rule. A backend that presigns
// sets NewPresigner, whose presigners sign requests to that backend, and
// PresignerName; the presign rules then send the requests they sign with
// HTTPClient. Subject.Skip names the rules a backend skips, each with the
// reason, such as a backend that keeps versions, which skips
// `Unsupported`, or an emulator that does not do as S3 does.
//
// # Suite
//
// Run runs each rule as a parallel subtest, on new stores each: the
// lifecycle sentinels, a restart that keeps the objects, Init with an
// ended context, an ended context before every method and during a
// body, nil inputs, Close waiting for and canceling a PutObject in
// flight, restarts racing calls and concurrent calls for -race; a round
// trip of every header field and the metadata, the ETag, the default
// Content-Type, empty objects, bodies of unknown length, overwrites,
// content lengths and body errors, objects and buckets not found, the
// configured bucket, conditions and their race, ranges, versions and
// parts; the key, bucket, metadata, header, range, argument and condition
// rules, each before any I/O; buckets, their listing and its arguments;
// listings with prefixes, delimiters, StartAfter and pages, their
// arguments and their tokens; copies within a bucket and across buckets,
// of odd keys, with and without Replace, over an object, onto
// themselves, of missing objects, their rules, the caller's input left
// unchanged, concurrent copies and errors that hold no data; and, with a
// presigner, uploads, downloads, wrong lengths, expiry, TTLs, missing
// objects, arguments, the lifecycle and an ended context. Every error is
// checked with errors.Is, and its full text where every backend makes it
// the same; the text starts with the Subject's Name, or PresignerName. No
// error may hold a key, a bucket name, metadata or a header value.
package conformance
