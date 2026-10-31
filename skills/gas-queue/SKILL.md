---
name: gas-queue
description: >
  Reference documentation for the gas/queue Go package
  (github.com/gasmod/gas/queue), the job queue service for the Gas ecosystem.
  Use this skill when writing, reviewing, or debugging Go code that uses
  gas/queue for async job/message queue processing with AWS SQS or the
  in-memory store. Covers the sqs and memory sub-packages, queuetest mock,
  gas.JobQueueProvider implementation, sentinel errors, DI wiring, configuration
  binding, enqueue options (delay, FIFO group, deduplication, attributes),
  dequeue with long-polling, ack/nack semantics, and custom endpoint support
  for ElasticMQ. Make sure to use this skill
  whenever working with job queues in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/queue; any code that imports gasmod/gas/queue
  or references gas.JobQueueProvider should trigger this skill.
---

# Gas Queue Package Reference

Job queue service for the Gas ecosystem. Provides `gas.JobQueueProvider`
implementations backed by AWS SQS and by process memory, plus a test mock. The
rules are SQS's, so code written against one store runs on the other.

```
import queue "github.com/gasmod/gas/queue"
import queuesqs "github.com/gasmod/gas/queue/sqs"
import queuemem "github.com/gasmod/gas/queue/memory"
import "github.com/gasmod/gas/queue/queuetest"
```

## Backends

| Backend | Package            | Service name       | Use case                                 |
|---------|--------------------|--------------------|------------------------------------------|
| SQS     | `gas/queue/sqs`    | `gas/queue/sqs`    | Production (AWS SQS, ElasticMQ, LocalStack) |
| Memory  | `gas/queue/memory` | `gas/queue/memory` | Tests and local development (SQS rules, no server) |

Both implement `gas.Service` and `gas.JobQueueProvider`; the SQS backend also
implements `gas.ReadyReporter`. Both add `NackAfter` and `Extend` beyond the
interface.

## JobQueueProvider Interface

Defined in the gas core package:

```go
type JobQueueProvider interface {
    Enqueue(ctx context.Context, queue string, payload []byte, opts ...EnqueueOption) error
    Dequeue(ctx context.Context, queue string, maxMessages int, wait time.Duration) ([]Job, error)
    Ack(ctx context.Context, queue string, job Job) error
    Nack(ctx context.Context, queue string, job Job) error
}
```

The `queue` argument is a **queue name** (1 to 80 ASCII letters, digits, `-`,
`_`; FIFO names end in `.fifo`) **or a queue URL**, as `queue.ParseQueue`
reads it. The SQS store resolves a name with `GetQueueUrl` (needs
`sqs:GetQueueUrl`) and caches the URL until Close. Errors name a queue by its
name, never its URL (which holds the account ID).

### Job

```go
type Job struct {
    ID            string
    ReceiptHandle string            // opaque token used by Ack/Nack
    Attributes    map[string]string // user attributes, then system attributes
    Body          []byte
}
```

System attributes (`queue.SystemAttributes()`): `ApproximateReceiveCount`,
`ApproximateFirstReceiveTimestamp`, `SentTimestamp`, `MessageGroupId`,
`MessageDeduplicationId`, `SequenceNumber`. They replace a user attribute of
the same name.

### EnqueueOption

```go
gas.WithDelay(d time.Duration)                 // 0 to 15m, rounded up to whole seconds; not on FIFO
gas.WithGroupID(id string)                     // required on FIFO; tenant on standard queues
gas.WithDedupeID(id string)                    // FIFO only; refused on standard queues
gas.WithJobAttributes(attrs map[string]string) // at most 10 user attributes
```

Implementations unpack options via `gas.ApplyEnqueueOptions(opts)` (or
`queue.NewMessage(payload, opts)`).

## Contract (every store)

- **Delivery is at least once.** Dequeue leases a message (hidden for the
  visibility timeout); Ack deletes it; Nack makes it visible again at once.
  Consumers must be idempotent.
- **Messages:** the payload is 1 byte or more of valid UTF-8 with only the
  characters SQS allows (base64 binary first), at most `queue.MaxPayloadSize`
  (1 MiB) including attributes. Attribute names may not be system attribute
  names or start with `AWS.`/`Amazon.`. A bad message is never stored.
- **Dequeue:** `maxMessages` 1 to 10, `wait` 0 to 20s (0 is a short poll),
  counted in whole seconds rounded up. **Out-of-range values are
  `ErrInvalidArgument`, never clamped.** No jobs and no error when none came.
- **FIFO:** `WithGroupID` is required; without `WithDedupeID`, the queue must
  have content-based deduplication. Dedupe window 5 minutes.
- **NackAfter / Extend** `(ctx, queue, job, d)`: end the lease `d` from now,
  0 to 12h. Nack, NackAfter and Extend need the message still leased under
  the job's receipt handle, else `ErrNotInFlight`.
- **Lifecycle:** `ErrNotInitialized` before the first successful Init,
  `ErrAlreadyInitialized` from Init while open, `ErrClosed` after Close until
  Init succeeds again. Close waits for calls in flight; a Dequeue in flight
  returns no jobs and `ErrClosed`, and makes what it received visible again.
- Stop the workers that call Dequeue, and let them Ack, before Close.
- Order of checks: arguments, then state, then context.

## Sentinel Errors

The root `queue` package defines (test with `errors.Is`):

```go
queue.ErrInvalidQueue       // neither a queue name nor a queue URL
queue.ErrInvalidPayload     // empty, invalid UTF-8, bad character, too large
queue.ErrInvalidAttribute   // count, name or value rule broken
queue.ErrInvalidArgument    // delay, ID, FIFO rule, maxMessages, wait, visibility
queue.ErrInvalidReceipt     // empty or foreign receipt handle
queue.ErrQueueNotFound      // queue does not exist
queue.ErrNotInFlight        // message not leased under the job's receipt handle
queue.ErrNotInitialized     // used before the first successful Init
queue.ErrAlreadyInitialized // Init on an open store
queue.ErrClosed             // used after Close
queue.ErrNilLogger          // constructor got a nil logger
queue.ErrNilConfig          // WithConfig(nil)
queue.ErrNoConfigProvider   // no WithConfig and a nil ConfigProvider
queue.ErrInvalidConfig      // config failed to bind, validate, or a store rule
```

Errors start with the service name, the operation and the quoted queue name,
such as `gas/queue/sqs: enqueue "orders": queue does not exist (QueueDoesNotExist)`.
No error holds a payload, attribute, ID, receipt handle, queue URL, endpoint
or credential.

## SQS Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

A nil logger is `queue.ErrNilLogger`; a nil ConfigProvider is accepted, and
Init then requires `WithConfig`.

### Options

| Option                    | Description                                                      |
|---------------------------|------------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Use this config (a copy, checked against the same tags) instead of binding |
| `WithClient(client API)`  | Use this client (`*sqs.Client` or a fake of `queuesqs.API`, which includes `ChangeMessageVisibilityBatch` and `GetQueueUrl`); region, endpoint, credentials and `maxretries` then have no effect. Nil fails Init with `ErrNilClient` |

### Lifecycle (gas.Service)

| Method  | Description                                                      |
|---------|------------------------------------------------------------------|
| `Name`  | Returns `queuesqs.ServiceName`, `"gas/queue/sqs"`                |
| `Init`  | Resolves config, builds the client from the AWS default chain, retrieves credentials within `optimeout` (`ErrNoCredentials` when none, `ErrNoRegion` when no region); sends no SQS request |
| `Close` | Refuses new calls and waits for calls in flight (a Dequeue finishes its receive, then releases what it got). If Close's context ends first, it cancels them and returns the context's error |

### Behavior

- **Enqueue** is SendMessage; **Dequeue** is ReceiveMessage (sends a wait of
  0 explicitly; applies `visibilitytimeout` unless 0); **Ack** is
  DeleteMessage; **Nack/NackAfter/Extend** are ChangeMessageVisibility.
- **Timeouts:** every request runs within its context and `optimeout`,
  SDK retries included; a Dequeue within its wait plus `optimeout`. Give a
  worker's Dequeue a context that outlives its wait.
- **No reply** wraps `queuesqs.ErrConnect` (`timed out`, `connection
  refused`, `unreachable`). An SQS error reply wraps `queuesqs.ErrCommand`
  with a reason and the AWS code; `ErrAccessDenied` and `ErrThrottled` are
  kinds of it, and QueueDoesNotExist, ReceiptHandleIsInvalid and
  MessageNotInflight also wrap the queue sentinels.
- **CheckReady** does no I/O: it fails only when the store is not
  initialized or its context has ended.
- **Client accessor:** `Client() *sqs.Client` returns the SDK client for
  calls beyond the interface (such as `CreateQueue`, `PurgeQueue`), or nil
  when not initialized or `WithClient` gave another `API`. Its errors are
  the SDK's, unredacted.

### Config

Section `queue`; keys `queue.region`, `queue.endpoint`, `queue.accesskeyid`,
`queue.secretaccesskey`, `queue.sessiontoken`, `queue.optimeout`,
`queue.visibilitytimeout`, `queue.maxretries`.

```go
type Config struct {
    Queue Settings
}

type Settings struct {
    Region            string        // empty = AWS default chain (AWS_REGION, profile); no us-east-1 default
    Endpoint          string        // http(s) URL with host and nothing after but "/" (e.g. ElasticMQ)
    AccessKeyID       string        // static key ID, used only with SecretAccessKey
    SecretAccessKey   Secret        // prints, marshals and logs as [redacted]
    SessionToken      Secret        // temporary credentials
    OpTimeout         time.Duration `validate:"gt=0"`          // default 10s
    VisibilityTimeout time.Duration `validate:"min=0,max=12h"` // 0 = queue's own (default); else 1s to 12h
    MaxRetries        int           `validate:"min=0,max=10"`  // default 2 (3 attempts)
}

func DefaultConfig() *Config
```

Static credentials apply only when both keys are set; with one missing, the
default chain is used and Init logs a warning naming the missing keys. The
old `WaitTimeSeconds` setting is gone: the wait is Dequeue's argument. An
`http` endpoint sends the session token and payloads in clear text; use it
only for an emulator.

## Memory Backend

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error) // WithConfig, WithClock
func (s *Service) CreateQueue(ctx context.Context, id string, cfg QueueConfig) error
```

A queue exists only once `CreateQueue` creates it; other methods on a missing
queue return `ErrQueueNotFound`. `QueueConfig{VisibilityTimeout, Delay,
ContentBasedDeduplication}`; queue visibility 0 means 30s. Creating an
existing queue with the same config is not an error; another config is
`queuemem.ErrQueueExists`. Queues outlive Close. Enqueue past `maxmessages`
or `maxbytes` is `queuemem.ErrQueueFull`. `WithClock(c Clock)` lets a test move
time by hand (Clock has `Now() time.Time`).

Config section `queue`: `VisibilityTimeout` (default 0), `MaxMessages`
(default 100000), `MaxBytes` (default 256 MiB, at least 1 MiB).

## Test Mock

```go
type MockQueue struct {
    EnqueueFn func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error
    DequeueFn func(ctx context.Context, queue string, maxMessages int, wait time.Duration) ([]gas.Job, error)
    AckFn     func(ctx context.Context, queue string, job gas.Job) error
    NackFn    func(ctx context.Context, queue string, job gas.Job) error
}
```

With a field nil, Dequeue returns no jobs and the rest return nil. Each call
is recorded; Enqueue's options are resolved into a `queuetest.Options`. The
mock checks no argument; to test against the queue rules, use the memory
store.

| Method                  | Description                                   |
|-------------------------|-----------------------------------------------|
| `Calls() []Call`        | Deep copy of the recorded calls, in order     |
| `CallCount(method) int` | Count calls by method name (e.g. `"Enqueue"`) |
| `Reset()`               | Forget all recorded calls                     |

Methods may run from several goroutines; setting an `Fn` field while a
method runs is a data race.

## DI Wiring Patterns

Register the backend under `gas.JobQueueProvider`: the container resolves a
dependency by its exact registered type. `New` also needs a registered
`gas.ConfigProvider` (unless `WithConfig` is given) and `gas.Logger`.

### SQS backend

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New()),
)
```

### With explicit config (ElasticMQ)

```go
cfg := queuesqs.DefaultConfig()
cfg.Queue.Region = "eu-west-1"
cfg.Queue.Endpoint = "http://localhost:9324"

app := gas.NewApp(
    gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New(queuesqs.WithConfig(cfg))),
)
```

### Consuming via gas.JobQueueProvider

```go
type Service struct {
    queue gas.JobQueueProvider
}

func (s *Service) Schedule(ctx context.Context, payload []byte) error {
    return s.queue.Enqueue(ctx, "orders", payload, gas.WithDelay(30*time.Second))
}

func (s *Service) Work(ctx context.Context) error {
    jobs, err := s.queue.Dequeue(ctx, "orders", 10, 20*time.Second)
    if err != nil {
        return err
    }
    for _, job := range jobs {
        if handle(job) == nil {
            err = s.queue.Ack(ctx, "orders", job)
        } else {
            err = s.queue.Nack(ctx, "orders", job)
        }
        if err != nil {
            return err
        }
    }
    return nil
}
```

### Using the test mock

```go
mock := &queuetest.MockQueue{
    EnqueueFn: func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error {
        return nil
    },
}
// inject mock as gas.JobQueueProvider; assert with mock.CallCount("Enqueue")
```
