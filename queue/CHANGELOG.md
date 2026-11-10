# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.1] - 2026-11-10

### Changed

- Dependencies updated, and the internal modules required at v0.6.1.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around one contract, Amazon SQS's rules,
that every store keeps, written in the `queue` package's `doc.go` and
checked by a shared conformance suite that runs against the memory
store and ElasticMQ, and, opt-in, LocalStack. Each package's `doc.go`
is its behavioral contract and lists its errors verbatim; read it
before upgrading.

### Added

- **`queue/memory`, an in-process store** with SQS's rules and limits,
  for tests and local development: `CreateQueue` with a `QueueConfig`
  (visibility timeout, delay, content-based deduplication), FIFO groups,
  a 5-minute dedupe window, receive counts, `MaxMessages` and `MaxBytes`
  limits (`ErrQueueFull`), `ErrQueueExists`, and `WithClock` for a clock
  a test moves by hand. Queues and their messages outlive `Close`.
- **One store contract.** A queue is named by a queue name or a queue
  URL; delivery is at least once; `Enqueue` checks the message before it
  sends anything; `Dequeue` takes `maxMessages` 1 to 10 and a wait 0 to
  20s, a wait of 0 being a short poll; `Ack` deletes, `Nack` makes the
  message visible at once.
- `NackAfter` and `Extend` on both stores, which set a leased message's
  visibility to end a duration from now, 0 to 12h, rounded up to a
  whole second.
- `Service.Config` on both stores returns a copy of the configuration
  the last successful `Init` resolved, defaults included, or the zero
  `Config` before one; `Close` and a failed `Init` keep it. The memory
  store reports `VisibilityTimeout` as resolved, before `Init` rounds it
  up; the SQS store's secret key and session token stay a `Secret`.
- In `queue`: the limits (`MaxPayloadSize`, `MaxAttributes`,
  `MaxAttributeNameLength`, `MaxQueueNameLength`, `MaxIDLength`,
  `MaxDelay`, `MaxMessages`, `MaxWait`, `MaxVisibilityTimeout`,
  `DedupeWindow`), `FIFOSuffix`, the system attribute names and
  `SystemAttributes`, `IsReservedAttribute`, `Queue` and `ParseQueue`,
  `Message`, `NewMessage` and `CheckMessage`, `CheckDequeue`,
  `CheckVisibilityTimeout`, `CheckReceipt` and `WholeSeconds`.
- Sentinel errors in `queue`: `ErrInvalidQueue`, `ErrInvalidPayload`,
  `ErrInvalidAttribute`, `ErrInvalidArgument`, `ErrInvalidReceipt`,
  `ErrQueueNotFound`, `ErrNotInFlight`, `ErrNotInitialized`,
  `ErrAlreadyInitialized`, `ErrNilLogger`, `ErrNilConfig`,
  `ErrNoConfigProvider` and `ErrInvalidConfig`.
- `sqs` sentinels `ErrNoRegion`, `ErrNoCredentials`, `ErrNilClient`,
  `ErrConnect`, `ErrCommand`, `ErrAccessDenied` and `ErrThrottled`: a
  failed request carries a fixed reason told by the network error or
  the AWS error code, never the AWS SDK's error.
- `sqs` settings: `SessionToken`, `OpTimeout` (the bound of each
  request, retries included, default 10s) and `MaxRetries` (0 to 10,
  default 2), which holds even when `AWS_MAX_ATTEMPTS` or `max_attempts`
  is set.
- `sqs.ServiceName`, `sqs.DefaultOpTimeout`, `DefaultVisibilityTimeout`,
  `DefaultMaxRetries` and `MaxRetries`; `memory.ServiceName` and its
  defaults.
- `queuetest.Options` holds an `Enqueue`'s options, resolved, as a
  recorded call holds them.

### Changed

- **BREAKING: the queue argument is a queue name or a queue URL.** The
  `sqs` store resolves a name with `GetQueueUrl`, which needs the
  `sqs:GetQueueUrl` permission, and keeps the URL until `Close`. An
  identifier that is neither is `ErrInvalidQueue`; a queue that does not
  exist is `ErrQueueNotFound`.
- **BREAKING: messages are checked before they are sent.** A payload is
  1 byte or more of valid UTF-8 with only the characters SQS allows; a
  message is at most 1 MiB with its attributes; at most 10 attributes,
  with SQS's name rules and no system attribute's name. A FIFO queue
  needs a group ID and takes no per-message delay; a standard queue
  takes no dedupe ID. Invalid UTF-8 used to be stored mangled and
  reported as failed, so a retry stored it twice.
- **BREAKING: arguments out of bounds are errors, never clamped.**
  `maxMessages` outside 1 to 10, a wait outside 0 to 20s, a delay
  outside 0 to 15m and a visibility timeout outside 0 to 12h are
  `ErrInvalidArgument`. `sqs` used to clamp `maxMessages` with a warning.
  A delay, a wait and a visibility timeout are rounded up to whole
  seconds; a delay was truncated.
- **BREAKING: a wait of 0 is a short poll.** `sqs` sends
  `"WaitTimeSeconds":0`, so a queue with a long-poll attribute still
  answers at once; it used to fall back to the `WaitTimeSeconds`
  setting.
- **BREAKING: lifecycle sentinels.** Before the first successful `Init`
  every method returns `queue.ErrNotInitialized` (the `sqs` methods
  panicked); `Init` on an initialized store returns
  `queue.ErrAlreadyInitialized`; after `Close`, until `Init` succeeds
  again, `queue.ErrClosed`. `Close` then `Init` restarts a store.
- **BREAKING: `queue.ErrClosed` is `service closed`** (was
  `queue: service is closed`). Every error a store returns starts with
  its service name, the operation and the queue name, quoted, such as
  `gas/queue/sqs: enqueue "orders": queue does not exist
  (QueueDoesNotExist)`, and wraps a sentinel.
- **BREAKING: `Close` drains.** It refuses new calls and returns once
  every call in flight has returned. An `sqs` `Dequeue` in flight
  finishes its receive, makes what it received visible again with one
  `ChangeMessageVisibilityBatch`, and returns `queue.ErrClosed`. When
  `Close`'s context ends first, it cancels the calls in flight and
  returns an error wrapping the context's error. `Close` used to only
  set a flag.
- **BREAKING: every method honors its context**, and returns the
  context's error, wrapped, when it has ended before the call.
- **BREAKING: `sqs.Settings.Region` has no default** and is not
  required: without it the AWS SDK's default chain decides, such as
  `AWS_REGION`, and a region the chain does not find fails `Init` with
  `sqs.ErrNoRegion`. `DefaultConfig` no longer sets `us-east-1`.
- **BREAKING: `sqs.Settings.VisibilityTimeout` defaults to 0**, which
  sends none, so the queue's own visibility timeout applies (it was
  30s). A value must be 0 or 1s to 12h.
- **BREAKING: `sqs.Settings.SecretAccessKey` is a `sqs.Secret`**, an
  alias of `gas.Secret`. Configuration binds into it as into a string.
- **BREAKING: `sqs.Settings.Endpoint` must be an `http` or `https` URL
  with a host and nothing after it but `/`.**
- **BREAKING: `sqs.WithClient` takes the exported `sqs.API`**, which
  adds `ChangeMessageVisibilityBatch` and `GetQueueUrl` to the methods
  the unexported interface had. A nil client fails `Init` with
  `sqs.ErrNilClient`.
- **BREAKING: the constructor checks the logger.** A nil logger fails
  with `queue.ErrNilLogger`; a nil `ConfigProvider` is accepted, and
  `Init` then requires `WithConfig`.
- **BREAKING: a configuration given to `WithConfig` is validated** as a
  bound one is, copied, and never changed; a nil one is
  `queue.ErrNilConfig`. `Init` resolves the configuration afresh each
  time it runs.
- With only one of the static keys set, `sqs` uses the default chain
  and logs `static credentials incomplete, using the default chain` at
  warn level. A failure to retrieve credentials is `sqs.ErrNoCredentials`.
- `sqs` asks for the system attributes the queue package lists, sends a
  `ReceiveRequestAttemptId` on a FIFO queue, and refuses a reply with
  more messages than asked for or a message without an ID or a receipt
  handle. A job no longer holds a message attribute named like a system
  attribute, whose place the system attribute takes.
- `sqs.Service.CheckReady` does no I/O and fails only when the store is
  not initialized or its context has ended.
- `sqs.Service.Client` returns nil when the store is not initialized,
  after `Close` included; it returned the closed store's client.
- The logs change: `service initialized` and `service closed`, at info
  level. The `sqs` initialized log carries whether an endpoint and a
  client were given, the credential source and `op_timeout` besides the
  region.
- **BREAKING: `queuetest.MockQueue.Calls` is a method.** The exported
  `Calls` field is gone; `Calls()` returns a deep copy. `Enqueue` records
  its options resolved into an `Options` and copies of the payload and
  the attributes.

### Removed

- **BREAKING:** `sqs.Settings.WaitTimeSeconds`.
- **BREAKING:** `sqs.Config` no longer embeds `gas.WithGasEnv`, which
  core removes.
- The `claude-skill/gas-queue` directory.

### Fixed

- `sqs.Service` methods no longer panic with a nil pointer dereference
  before `Init`; they return `queue.ErrNotInitialized` (KI-036).
- A restart no longer races calls in flight.

### Security

- No error the stores make holds a payload, an attribute name or value,
  a group or dedupe ID, a receipt handle, a queue URL, which holds the
  AWS account ID, an endpoint or a credential; errors name the queue by
  its name. The AWS SDK's default logger, which writes raw transport
  errors holding the endpoint, is silenced.
- The secret key and the session token no longer print through fmt,
  encoding/json, YAML encoders or log/slog, which write them as
  `[redacted]`, and refuse gob encoding. The access key ID is never
  logged.
- The client `Init` builds reads no reply past 64 MiB, so an endpoint
  cannot make it read without bound.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `queue/v0.4.1` to `queue/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-queue` repository.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-queue` to `github.com/gasmod/gas/queue`, in
  queue/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in queue/v0.5.0, as core now uses generic methods.
- **BREAKING: `sqs.New` returns an error.** It now returns
  `func(gas.ConfigProvider, gas.Logger) (*Service, error)`, the
  `CONVENTIONS.md` shape. It cannot fail yet, so it returns nil; the change is
  the shape. Registering it with `gas.WithSingletonService` is unchanged,
  since the container accepts a trailing error. Code that calls the returned
  function directly must take the second result (gas#106).
- **BREAKING: `sqs.Config.Validate` is removed; the rules are validate
  tags.** `Settings.Region` carries `validate:"required"`,
  `VisibilityTimeout` `validate:"gte=0s"`, and `WaitTimeSeconds`
  `validate:"min=0,max=20"`. `ConfigProvider.Bind` already validates the
  struct it binds, so `Init` no longer runs a check of its own. A config
  passed with `WithConfig` is used as given, as `gas.Config` is when an App
  has no `ConfigProvider`. The error for an invalid bound config is now the
  validator's, under `gas/queue/sqs: config binding:`, instead of the
  hand-written message (gas#116).
- `sqs.Service` no longer logs an invalid configuration it also returns;
  the caller logs it once. It is returned as
  `gas/queue/sqs: invalid configuration: <cause>`.
- **BREAKING:** `sqs.Service` implements the context-aware `gas.Service`:
  `Init(ctx)` and `Close(ctx)`. `Init` loads the AWS configuration and
  fetches the credentials under the caller ctx. `Close` ignores ctx.
- `Init` fetches the AWS credentials, so a missing or broken credential setup
  (no static keys, no environment variables, profile, web identity, or
  instance role) fails startup with an error naming the credential lookup,
  instead of failing the first call. With the default chain this can make a
  network call at startup (for example to the instance metadata service).
  Not applied when `WithClient` supplies the client.

## 0.3.0 - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the package as published.

### Added

- **`sqs.Service`** — an AWS SQS-backed queue implementing `gas.Service`,
  `gas.JobQueueProvider`, and `gas.ReadyReporter` (`CheckReady` errors before
  `Init` and after `Close`, so callers can drain traffic during graceful
  shutdown). Constructed via `sqs.New()` for DI-based injection.
- **Enqueue options** — `gas.WithDelay` (initial visibility delay),
  `gas.WithGroupID` and `gas.WithDedupeID` (FIFO ordering and
  deduplication), and `gas.WithJobAttributes` (custom message attributes),
  applied through `gas.ApplyEnqueueOptions`.
- **Dequeue with long-polling** — `Dequeue` clamps `maxMessages` to SQS's
  1-10 range (logging a warning if clamped) and falls back to the
  configured `Queue.WaitTimeSeconds` when no explicit wait is given. Both
  system attributes (e.g. `SentTimestamp`, `ApproximateReceiveCount`) and
  message attributes are merged onto the returned `gas.Job.Attributes`.
- **Ack/Nack semantics** — `Ack` deletes the message from the queue; `Nack`
  zeroes the message's visibility timeout via `ChangeMessageVisibility`,
  making it immediately available for redelivery.
- **Configuration** — `Config`/`Settings`, bindable via `gas.ConfigProvider`
  when `WithConfig` is not supplied, covering `Queue.Region`,
  `Queue.Endpoint` (custom endpoint for ElasticMQ or other SQS-compatible
  services), `Queue.AccessKeyID`/`Queue.SecretAccessKey` (falling back to
  the default AWS credential chain when unset), `Queue.VisibilityTimeout`,
  and `Queue.WaitTimeSeconds`, all validated on `Init`.
- **`WithClient`** to inject a pre-configured SQS client (or any type
  satisfying the minimal `sqsClient` interface), and **`Client()`** to
  access the underlying `*sqs.Client` directly for advanced operations
  (`CreateQueue`, `PurgeQueue`, `GetQueueAttributes`, etc.) beyond the
  `gas.JobQueueProvider` interface.
- **`queue.ErrClosed`** sentinel error, returned by `Enqueue`, `Dequeue`,
  `Ack`, `Nack`, and `CheckReady` once the service has been closed.
- **`queuetest` package** with `MockQueue`, a configurable mock of
  `gas.JobQueueProvider` that records calls (`CallCount`, `Reset`) and
  delegates to per-method `Fn` fields when set.

[Unreleased]: https://github.com/gasmod/gas/compare/queue/v0.6.1...HEAD
[0.6.1]: https://github.com/gasmod/gas/releases/tag/queue/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/queue/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/queue/v0.5.6
