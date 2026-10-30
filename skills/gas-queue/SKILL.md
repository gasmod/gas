---
name: gas-queue
description: >
  Reference documentation for the gas/queue Go package
  (github.com/gasmod/gas/queue) — the job queue service for the Gas ecosystem.
  Use this skill when writing, reviewing, or debugging Go code that uses
  gas/queue for async job/message queue processing with AWS SQS. Covers the sqs
  sub-package, queuetest mock, gas.JobQueueProvider implementation, sentinel
  errors, DI wiring, configuration binding, enqueue options (delay, FIFO group,
  deduplication, attributes), dequeue with long-polling, ack/nack semantics,
  and custom endpoint support for ElasticMQ. Make sure to use this skill
  whenever working with job queues in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/queue — any code that imports gasmod/gas/queue
  or references gas.JobQueueProvider should trigger this skill.
---

# Gas Queue Package Reference

Job queue service for the Gas ecosystem. Provides a `gas.JobQueueProvider`
implementation backed by AWS SQS, plus a reusable test mock.

```
import queue "github.com/gasmod/gas/queue"
import queuesqs "github.com/gasmod/gas/queue/sqs"
import "github.com/gasmod/gas/queue/queuetest"
```

## Backends

| Backend | Package          | Service name    | Use case                          |
|---------|------------------|-----------------|-----------------------------------|
| SQS     | `gas/queue/sqs`  | `gas/queue/sqs` | Production (AWS SQS / ElasticMQ) |

The SQS backend implements `gas.Service`, `gas.JobQueueProvider`, and
`gas.ReadyReporter`. `CheckReady` returns `queue.ErrClosed` after `Close`,
and an error that wraps no sentinel (`gas/queue/sqs: not initialized`) when
there is no client yet, before `Init` without `WithClient`; otherwise it
returns nil. The check is local-only — it does not call AWS — so readiness probes
remain cheap and independent of regional SQS availability.

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

The `queue` parameter in every method is the **SQS queue URL** directly.

### Job

```go
type Job struct {
    ID            string
    ReceiptHandle string            // opaque token used by Ack/Nack
    Attributes    map[string]string // merged system + message attributes
    Body          []byte
}
```

### EnqueueOption

```go
gas.WithDelay(d time.Duration)              // initial visibility delay
gas.WithGroupID(id string)                  // FIFO ordering (SQS: MessageGroupId)
gas.WithDedupeID(id string)                 // deduplication (SQS: MessageDeduplicationId)
gas.WithJobAttributes(attrs map[string]string) // provider-specific metadata
```

Implementations unpack options via `gas.ApplyEnqueueOptions(opts)`.

## Sentinel Errors

The root `queue` package defines sentinel errors:

```go
queue.ErrClosed // returned when an operation is attempted on a closed service
```

## SQS Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

`New` captures options and returns a DI-injectable constructor. The returned
func receives `gas.ConfigProvider` and `gas.Logger` from the DI container.

### Options

| Option                         | Description                                                      |
|--------------------------------|------------------------------------------------------------------|
| `WithConfig(cfg *Config)`      | Set configuration explicitly (skips config binding from DI)      |
| `WithClient(client sqsClient)` | Inject a pre-configured SQS client (testing or custom AWS creds) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                           |
|---------|-------------|-------------------------------------------------------|
| `Name`  | `() string` | Returns `"gas/queue/sqs"`                             |
| `Init`  | `(ctx context.Context) error` | Binds config (unless `WithConfig`), creates AWS SQS client, fetches AWS credentials (ctx bounds both); fails when none can be found. Client and credentials are skipped when `WithClient` was given |
| `Close` | `(ctx context.Context) error` | Marks service as closed (ctx unused) |

### Behavior

- **Enqueue:** Calls SQS `SendMessage`. Supports delay (sent as whole
  seconds, truncated), FIFO group ID, deduplication ID, and string message
  attributes via `EnqueueOption`.
- **Dequeue:** Calls SQS `ReceiveMessage` with long-polling. `maxMessages` is
  clamped to 1-10 (SQS hard limit). When `wait` truncates to 0 whole
  seconds, uses the configured `WaitTimeSeconds`. Every receive applies the
  configured `VisibilityTimeout`. Merges SQS system attributes and string
  message attributes into `Job.Attributes`; a message attribute wins on a
  shared name.
- **Ack:** Calls SQS `DeleteMessage` using `job.ReceiptHandle`.
- **Nack:** Calls SQS `ChangeMessageVisibility` with timeout 0, making the
  message immediately available for reprocessing.
- **No upfront connection:** The AWS SQS client is stateless HTTP — Init
  creates the client but does not verify connectivity. Connection errors
  surface at call time.
- **All methods return `queue.ErrClosed`** when the service has been closed.
- **Client accessor:** `Client() *sqs.Client` returns the underlying SDK
  client for calls beyond the interface (such as `CreateQueue`), or nil when
  `WithClient` injected a client of another type.

### Config

```go
type Config struct {
    gas.WithGasEnv
    Queue Settings
}

type Settings struct {
    Region            string        `validate:"required"`     // default "us-east-1"
    Endpoint          string        // optional custom endpoint (e.g. ElasticMQ)
    AccessKeyID       string        // static AWS key; empty = default credential chain
    SecretAccessKey   string        // static AWS secret; empty = default credential chain
    VisibilityTimeout time.Duration `validate:"gte=0s"`        // default 30s
    WaitTimeSeconds   int           `validate:"min=0,max=20"` // default 20
}

func DefaultConfig() *Config
```

The rules are `validate` tags, which `ConfigProvider.Bind` checks when `Init`
binds the config over `DefaultConfig()`. A config passed with `WithConfig` is
used as given, with no defaults filled in, so build it from `DefaultConfig()`.
There is no `Validate` method.

## Test Mock

The `queuetest` package provides `MockQueue`, a configurable mock of
`gas.JobQueueProvider` for use in unit tests.

```go
import "github.com/gasmod/gas/queue/queuetest"
```

### MockQueue

```go
type MockQueue struct {
    EnqueueFn func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error
    DequeueFn func(ctx context.Context, queue string, maxMessages int, wait time.Duration) ([]gas.Job, error)
    AckFn     func(ctx context.Context, queue string, job gas.Job) error
    NackFn    func(ctx context.Context, queue string, job gas.Job) error
    Calls     []Call
}
```

Each method delegates to its `Fn` field if set, otherwise returns zero value.
All calls are recorded in `Calls` for assertions. Thread-safe.

| Method                  | Description                                   |
|-------------------------|-----------------------------------------------|
| `Reset()`               | Clear all recorded calls                      |
| `CallCount(method) int` | Count calls by method name (e.g. `"Enqueue"`) |

## DI Wiring Patterns

Register the backend under `gas.JobQueueProvider`: the container resolves a
dependency by its exact registered type, so a registration under
`*queuesqs.Service` does not satisfy a constructor that takes
`gas.JobQueueProvider`. `New` also needs a registered `gas.ConfigProvider`
and `gas.Logger`.

### SQS backend

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New()),
)
```

### With explicit config

```go
cfg := queuesqs.DefaultConfig()
cfg.Queue.Region = "eu-west-1"
cfg.Queue.Endpoint = "http://localhost:9324"

app := gas.NewApp(
    gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New(queuesqs.WithConfig(cfg))),
)
```

### Consuming via gas.JobQueueProvider

Services receive the queue through the provider interface, never importing
gas/queue backends directly:

```go
type Service struct {
    queue gas.JobQueueProvider
}

func New(queue gas.JobQueueProvider) *Service {
    return &Service{queue: queue}
}

func (s *Service) Schedule(ctx context.Context, queueURL string, payload []byte) error {
    return s.queue.Enqueue(ctx, queueURL, payload, gas.WithDelay(30*time.Second))
}
```

### Using the test mock

```go
mock := &queuetest.MockQueue{}
mock.EnqueueFn = func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error {
    return nil
}

// inject mock as gas.JobQueueProvider in tests
```
