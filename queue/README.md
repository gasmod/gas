# gas/queue

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/queue.svg)](https://pkg.go.dev/github.com/gasmod/gas/queue) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=queue/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [All modules](../README.md#modules) · [Examples](../example/README.md)

Job queue service for the [Gas](../README.md) framework. Provides a `gas.JobQueueProvider` implementation
backed by AWS SQS, plus a test mock for use in unit tests.

## Install

```bash
go get github.com/gasmod/gas/queue
```

`sqs.New()` returns a DI constructor that takes `gas.ConfigProvider` and `gas.Logger`, so register both alongside the queue (see [gas/config](../config/README.md) and [gas/log](../log/README.md)).

## Backends

| Backend | Package                           | Use case                          |
|---------|-----------------------------------|-----------------------------------|
| SQS     | `github.com/gasmod/gas/queue/sqs` | Production (AWS SQS / ElasticMQ) |

The SQS backend implements `gas.Service`, `gas.JobQueueProvider`, and
`gas.ReadyReporter` (returns an error before `Init` and after `Close` so
callers can drain traffic during graceful shutdown).

## Usage

### SQS backend

```go
package main

import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	queuesqs "github.com/gasmod/gas/queue/sqs"
)

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		gas.WithSingletonService[gas.JobQueueProvider](queuesqs.New()),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

With custom configuration:

```go
cfg := queuesqs.DefaultConfig()
cfg.Queue.Region = "eu-west-1"
cfg.Queue.Endpoint = "http://localhost:9324" // ElasticMQ

queuesqs.New(queuesqs.WithConfig(cfg))
```

With a pre-configured AWS client:

```go
queuesqs.New(queuesqs.WithClient(mySQSClient))
```

### Dependency injection

Services receive the queue through `gas.JobQueueProvider` via constructor injection:

```go
type Service struct {
	queue gas.JobQueueProvider
}

func New(queue gas.JobQueueProvider) *Service {
	return &Service{queue: queue}
}

func (s *Service) Init() error {
	ctx := context.Background()
	_ = s.queue.Enqueue(ctx, "https://sqs.us-east-1.amazonaws.com/123/my-queue", []byte(`{"task":"run"}`))
	return nil
}
```

### Enqueue options

```go
s.queue.Enqueue(ctx, queueURL, payload,
	gas.WithDelay(10*time.Second),          // initial visibility delay
	gas.WithGroupID("order-123"),           // FIFO ordering
	gas.WithDedupeID("unique-id"),          // deduplication
	gas.WithJobAttributes(map[string]string{"env": "prod"}),
)
```

### Worker loop

```go
for {
	jobs, err := s.queue.Dequeue(ctx, queueURL, 10, 20*time.Second)
	if err != nil {
		log.Error("dequeue failed").Err("error", err).Send()
		continue
	}

	for _, job := range jobs {
		if err := process(job); err != nil {
			_ = s.queue.Nack(ctx, queueURL, job) // make immediately available for retry
			continue
		}
		_ = s.queue.Ack(ctx, queueURL, job) // remove from queue
	}
}
```

## Config

If `WithConfig` is not provided, the backend automatically binds configuration from the `gas.ConfigProvider` injected
via DI. This lets you drive queue settings from environment variables or a config file without any explicit wiring.

### SQS config

| Field                     | Default     | Description                                                    |
|---------------------------|-------------|----------------------------------------------------------------|
| `Queue.Region`            | `us-east-1` | AWS region                                                     |
| `Queue.Endpoint`          |             | Custom endpoint URL (e.g. ElasticMQ); empty = default AWS      |
| `Queue.AccessKeyID`       |             | Static AWS access key ID; empty = default credential chain     |
| `Queue.SecretAccessKey`   |             | Static AWS secret access key; empty = default credential chain |
| `Queue.VisibilityTimeout` | `30s`       | How long a dequeued message stays invisible to other consumers |
| `Queue.WaitTimeSeconds`   | `20`        | Long-poll duration for ReceiveMessage (0-20, SQS hard limit)   |

## Sentinel Errors

The root `queue` package defines a sentinel error used by all backends:

```go
queue.ErrClosed // returned when an operation is attempted on a closed service
```

## Testing

The `queuetest` package provides a mock implementation of `gas.JobQueueProvider`:

```go
import "github.com/gasmod/gas/queue/queuetest"

mock := &queuetest.MockQueue{}
mock.EnqueueFn = func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error {
	return nil
}

// pass mock as gas.JobQueueProvider
// assert calls:
if mock.CallCount("Enqueue") != 1 {
	t.Error("expected one Enqueue call")
}
```
