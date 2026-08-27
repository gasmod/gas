# Gas

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas.svg)](https://pkg.go.dev/github.com/gasmod/gas) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

**Gas is a modular Go framework for building micro-SaaS applications.** It provides the plumbing every product
needs (dependency injection, routing, middleware, events, migrations, and service lifecycle management) so you
can spend your time on business logic instead of rebuilding the same wiring for each project.

📚 **[Documentation](https://gasmod.github.io/gas)** · [Getting started](https://gasmod.github.io/gas/start/getting-started/) · [Guides](https://gasmod.github.io/gas/guides/database/) · [API reference](https://pkg.go.dev/github.com/gasmod/gas)

## Quick start

```bash
go get github.com/gasmod/gas
```

```go
package main

import (
	"log"
	"net/http"

	"github.com/gasmod/gas"
)

func main() {
	app := gas.NewApp()

	app.Router().Handle("", http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

```bash
go run .
# listening on 0.0.0.0:8080
```

That is a complete Gas app. From there you write [services](https://gasmod.github.io/gas/start/first-service/)
and register modules the same way, letting the DI container wire them together.

## Modules

This repository is a monorepo. `github.com/gasmod/gas` is the core package, and each official module lives
beside it as a separate Go module, so `go get` pulls in only what you use.

| Module | Import path | What it gives you |
|---|---|---|
| **core** | `github.com/gasmod/gas` | App and Worker lifecycle, DI container, router, middleware, events, provider interfaces |
| [**auth**](auth/README.md) | `github.com/gasmod/gas/auth` | JWT, server-side sessions, API keys, and single-use tokens |
| [**cache**](cache/README.md) | `github.com/gasmod/gas/cache` | Key-value caching, in-memory or Valkey (Redis-compatible) |
| [**config**](config/README.md) | `github.com/gasmod/gas/config` | Config from env vars, JSON, `.env`, and AWS Secrets Manager, bound to structs |
| [**database**](database/README.md) | `github.com/gasmod/gas/database` | `database/sql` and native pgx pools, transaction helpers, sqlc-friendly |
| [**email**](email/README.md) | `github.com/gasmod/gas/email` | Transactional email over AWS SES, including templated sends |
| [**log**](log/README.md) | `github.com/gasmod/gas/log` | Structured logging (zerolog or slog) and HTTP/OTLP log shipping |
| [**migrate**](migrate/README.md) | `github.com/gasmod/gas/migrate` | Service-owned database migrations with dirty-state detection and rollback |
| [**queue**](queue/README.md) | `github.com/gasmod/gas/queue` | Job queues backed by AWS SQS |
| [**storage**](storage/README.md) | `github.com/gasmod/gas/storage` | Object storage on S3 and S3-compatible services |
| [**template**](template/README.md) | `github.com/gasmod/gas/template` | Template storage: memory, directory, embedded FS, database, or composite |
| [**ui**](ui/README.md) | `github.com/gasmod/gas/ui` | HTML rendering with layouts and partials, static files, HTMX fragments |

No module imports another module's service package. They meet through provider interfaces declared in core, so
any of them can be replaced by your own implementation or a test mock without the others noticing.

### Versioning

All modules share a single version and are released together. A release tags the root module `vX.Y.Z` and each
submodule `<dir>/vX.Y.Z`, which is what `go get github.com/gasmod/gas/auth@vX.Y.Z` resolves against. Mixing
versions across modules is not supported, so upgrade them together.

Gas is pre-1.0: minor versions may contain breaking changes. See the [CHANGELOG](CHANGELOG.md).

## Examples

Five runnable applications live in [`example/`](example/README.md), from a bare hello world up to a full API
server using auth, database, storage, queue, email, and cache together.

```bash
cd example/hello-world && go run ./cmd
```

## Documentation

| Where | What |
|---|---|
| [gasmod.github.io/gas](https://gasmod.github.io/gas) | Guides, concepts, and getting started |
| [pkg.go.dev](https://pkg.go.dev/github.com/gasmod/gas) | API reference for every package |
| [`example/`](example/README.md) | Runnable applications |

Documentation lives in one place per kind, on purpose. Guides and concepts are on the site, API reference comes
from doc comments on pkg.go.dev, and each module's README is a signpost to both. Code blocks on the site are
imported from a Go module that compiles in CI, so they cannot drift from the framework.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow: conventional
commits, signed-off commits (DCO), tests with every change, and `make lint`.

```bash
make test-all   # go test -race in every module
make lint-all   # golangci-lint in every module
make build-all  # go build in every module
```

A `go.work` file at the root ties the modules together, so local changes to one module are picked up by the
others without a `replace` directive of your own.

## Community and policies

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Security policy](SECURITY.md): please report vulnerabilities privately, never in a public issue.
- [Changelog](CHANGELOG.md)

## License

[MIT](LICENSE) © Ahmed Kamal
