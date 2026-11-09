# gas/queue

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/queue.svg)](https://pkg.go.dev/github.com/gasmod/gas/queue) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=queue/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.dev) · [All modules](../README.md#modules)

Job queues for the [Gas](../README.md) framework, with two interchangeable stores that keep
one contract, Amazon SQS's rules: Amazon SQS (or an emulator such as ElasticMQ or LocalStack),
and an in-memory store for tests and local development. The interface is pull-based: consumers
run their own loop and acknowledge with Ack or Nack.

Implements `gas.JobQueueProvider`.

```bash
go get github.com/gasmod/gas/queue
```

```go
gas.WithSingletonService[gas.JobQueueProvider](sqs.New()),
```

Both stores take `gas.ConfigProvider` and `gas.Logger` from the container and read the `queue`
section, or take their configuration from `WithConfig`; the SQS store takes its region and
credentials from `queue.region` and the static keys, or else from the AWS SDK's default chain.
Each store's `Config()` returns the configuration its last successful `Init` resolved.

| Package | Provides |
|---|---|
| `queue` | The shared contract, limits and sentinel errors |
| `queue/sqs` | Amazon SQS store, and emulators via a custom endpoint |
| `queue/memory` | In-process store with SQS's rules, for tests and local development |
| `queue/queuetest` | Recording mock of `gas.JobQueueProvider` |

## Documentation

The full documentation is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Docs:** [queue](https://gasmod.dev/reference/queue/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/queue](https://pkg.go.dev/github.com/gasmod/gas/queue)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
