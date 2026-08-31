# gas/queue

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/queue.svg)](https://pkg.go.dev/github.com/gasmod/gas/queue) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=queue/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Job queues for the [Gas](../README.md) framework, backed by AWS SQS. The interface is
pull-based: consumers run their own loop and acknowledge with Ack or Nack.

Implements `gas.JobQueueProvider`.

```bash
go get github.com/gasmod/gas/queue
```

```go
gas.WithSingletonService[gas.JobQueueProvider](sqs.New()),
```

`sqs.New()` needs `gas.ConfigProvider` and `gas.Logger`.

| Package | Provides |
|---|---|
| `queue/sqs` | AWS SQS, and ElasticMQ via a custom endpoint |
| `queue/queuetest` | Recording mock of `gas.JobQueueProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Run background jobs](https://gasmod.github.io/gas/guides/background-jobs/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/queue](https://pkg.go.dev/github.com/gasmod/gas/queue)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
