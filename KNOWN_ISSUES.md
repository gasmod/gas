# Known issues

Defects, test gaps, and debts found in Gas that are not fixed by the change
that found them. An entry stays here until the fix is merged; the fix removes
the entry in the same change, and, when the fix is visible to users, its
`CHANGELOG.md` entry records what was fixed.

Each entry has a stable ID (`KI-NNN`, never reused), and these fields:

- **Status**: `open`, `investigating`, `decision needed` (a design decision
  is pending), or `fix in progress` with the branch or PR.
- **Area**: the module and package.
- **Found**: the date, and how it was found.
- **Detail**: what is wrong, with the evidence (error text, file and line,
  input that triggers it).
- **Next step**: what has to happen to close it.

## KI-082: valkey-go allocates a reply's declared length without a bound

- **Status**: open
- **Area**: `cache/valkey` (upstream, `github.com/valkey-io/valkey-go`
  v1.0.79).
- **Found**: 2026-10-31, v0.6.0 rewrite of the cache module.
- **Detail**: valkey-go reads a bulk reply's length and allocates it before
  it reads the reply: `bs := make([]byte, length)` in `readB`
  (`resp.go:214` of v1.0.79), and an array's with
  `make([]ValkeyMessage, length)` in `readA` (`resp.go:241`), with no upper
  bound and no check for a negative length other than -1. A hostile server,
  or a man-in-the-middle on a plain-text connection, can announce a huge
  length and panic the process, on valkey-go's own goroutine when the
  connection is pipelining, or make it allocate until it runs out of
  memory. The store cannot catch it. `cache/valkey/doc.go:199-207`
  documents it and tells users to use TLS.
- **Next step**: report it upstream; until it is fixed, use TLS for a
  server outside the host or its private network.

## KI-083: `Router.Use` has no owner, so a restart applies it again

- **Status**: open
- **Area**: core, `router.go`.
- **Found**: 2026-10-31, v0.6.0 rewrite of the core package.
- **Detail**: a top-level `Use` appends to `r.pendingMW`
  (`router.go:328`) with no service attached, unlike named middleware,
  which `Register` records with its service. A service that calls
  `Router.Use` in Init calls it again when `RestartService` re-runs Init,
  so the middleware is applied once more on every restart, and the kill
  switch cannot remove it. The `Use` godoc (`router.go:302-306`) and the
  package documentation (`doc.go:181-184`) state it; it is not fixed.
- **Next step**: give `Use` an owner, as named middleware has, so a
  restart replaces a service's middleware rather than adding to it; or
  reject a `Use` from a service's Init and point to named middleware.

## KI-084: the config environment provider without a prefix loads every variable

- **Status**: decision needed
- **Area**: `config`, `provider_env.go`, `config.go`.
- **Found**: 2026-10-30, v0.6.0 rewrite of the config module.
- **Detail**: without `WithEnvPrefix`, `EnvProvider` loads every variable
  of the process environment it keeps (`config/provider_env.go:65-77`),
  and `config.New` with no provider adds one with no prefix
  (`config/config.go:79-81`). Other tools' API keys and tokens, a
  `DATABASE_URL` and the like become config keys. Printing is redacted:
  `String`, `Format` and `LogValue` give counts only
  (`config/config.go:346-371`). But `Values()` (`config/config.go:336`)
  and `Get` (`config/config.go:291`) return them. Probe: with a fake
  `KIPROBE_OTHER_TOOL_API_KEY` set in code, `config.New()` then `Load`
  makes `Get("kiprobe_other_tool_api_key")` return the value and
  `Values()` hold the key. The `EnvProvider` and `Values` godocs warn
  against it. During the v0.6.0 rewrite a real token was exposed this
  way.
- **Next step**: decide whether the env provider requires a prefix by
  default, which is a breaking change, or keeps the warning only.

## KI-086: the transfer manager logs a failed completion outside the store's logger

- **Status**: open
- **Area**: `storage/s3` (upstream,
  `github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager` v0.4.19).
- **Found**: 2026-10-31, v0.6.0 rewrite of the storage module.
- **Detail**: when `CompleteMultipartUpload` fails, the transfer manager
  calls `log.Printf("failed to complete multipart upload for upload ID %v:
  %v", u.uploadID, err)` with the standard library's `log` package
  (`api_op_UploadObject.go:1226` of v0.4.19), which writes to standard
  error outside the store's logger. The error text is this package's, as
  the store's API wrapper classifies it. `u.uploadID` is a `*string`
  (`api_op_UploadObject.go:999`), so `%v` prints a pointer address, not
  the ID. A version that printed the ID would leak it to standard error.
  `storage/s3/doc.go:168-174` documents it and says the version is pinned.
- **Next step**: re-check the log call on every bump of the transfer
  manager; report it upstream.

## KI-087: `storage/dir/internal/vfs` failure paths are not covered

- **Status**: open
- **Area**: `storage/dir`, `internal/vfs` tests.
- **Found**: 2026-10-31, v0.6.0 rewrite of the storage module.
- **Detail**: statement coverage of `internal/vfs` is 94.1%
  (`go test -coverpkg=./internal/vfs ./...` in `storage/dir`). What is
  left is mostly the failure of a file operation: an fstat, a read, a
  close, a sync or an flock that fails, such as `writeSynced`'s write,
  sync and close branches (`storage/dir/internal/vfs/fs.go:113-134`,
  76.9%), `OpenObject`'s unlock failure (`bucket.go:155`, 73.3%),
  `lockBlocking` (`lock_unix.go:30`, 71.4%) and `retryPending`
  (`fs.go:80`, 60.0%). A real file system does not fail these on demand,
  so the tests cannot reach them.
- **Next step**: add a fault-injection seam for the file operations and
  test each failure path.

## KI-088: SQS and SES behaviors not verified against real AWS

- **Status**: open
- **Area**: `queue/sqs`, `email/ses` tests.
- **Found**: 2026-10-31, v0.6.0 rewrites of the queue and email modules.
- **Detail**: the tests run on in-process fakes, and these behaviors of the
  real services were not checked:
  - `queue/sqs`: whether SQS fair queues return `MessageGroupId` on a
    standard queue. The store sends a group ID on a standard queue
    (`queue/sqs/ops.go:103-104`) and requests the system attribute
    (`queue/sqs/ops.go:258-271`); `queue/doc.go:133` and `:152-153` say
    it comes back.
  - `email/ses`: what SES does with an ASCII subject over 998 bytes, which
    is sent unfolded (`mime.BEncoding.Encode` returns ASCII unchanged,
    `email/ses/send.go:169`), and whether it accepts a horizontal tab in a
    display name, which the rules allow (`email/doc.go:142-146`).
  - `email/ses`: the end-to-end test on aws-ses-v2-local
    (`email/ses/test/sesv2local_test.go`) runs only with
    `GAS_TEST_SES_LOCAL=1`, and installs the emulator from npm in a Node
    container; it was not run.
- **Next step**: run each case against real AWS, and the aws-ses-v2-local
  test, then pin the observed behavior in a test or the documentation.
