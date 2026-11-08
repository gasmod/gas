# Gas

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas.svg)](https://pkg.go.dev/github.com/gasmod/gas) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

**Gas is a modular Go framework for building micro-SaaS applications.** It provides the plumbing every product
needs (dependency injection, routing, middleware, events, migrations, and service lifecycle management) so you
can spend your time on business logic instead of rebuilding the same wiring for each project.

📚 **[Documentation](https://gasmod.github.io/gas)** · [Getting started](https://gasmod.github.io/gas/start/installation/) · [Modules](https://gasmod.github.io/gas/modules/overview/) · [API reference](https://pkg.go.dev/github.com/gasmod/gas)

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

	app.Router().Handle(nil, http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

```bash
go run .
# ... INFO server started environment=development addr=0.0.0.0:8080
```

That is a complete Gas app. From there you write [services](https://gasmod.github.io/gas/core/services/)
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
| [**database**](database/README.md) | `github.com/gasmod/gas/database` | PostgreSQL, MySQL, and SQLite over `database/sql`, or a native pgx pool, with transaction helpers |
| [**email**](email/README.md) | `github.com/gasmod/gas/email` | Transactional email over Amazon SES or an in-memory outbox, including templated sends |
| [**log**](log/README.md) | `github.com/gasmod/gas/log` | Structured logging over slog or zerolog, and log shipping over HTTP, such as OTLP/JSON |
| [**migrate**](migrate/README.md) | `github.com/gasmod/gas/migrate` | Service-owned database migrations with checksums, dirty-state detection, and rollback |
| [**queue**](queue/README.md) | `github.com/gasmod/gas/queue` | Job queues on Amazon SQS or in memory |
| [**storage**](storage/README.md) | `github.com/gasmod/gas/storage` | Object storage on S3, S3-compatible services, a local directory, or memory |
| [**template**](template/README.md) | `github.com/gasmod/gas/template` | Template storage: memory, directory, embedded FS, database, or composite |
| [**ui**](ui/README.md) | `github.com/gasmod/gas/ui` | HTML rendering with layouts and partials, static files, HTMX fragments |

No module imports another module's service package. They meet through provider interfaces declared in core, so
any of them can be replaced by your own implementation or a test mock without the others noticing.

### Versioning

All modules share a single version and are released together. A release tags the root module `vX.Y.Z` and each
submodule `<dir>/vX.Y.Z`, which is what `go get github.com/gasmod/gas/auth@vX.Y.Z` resolves against. Mixing
versions across modules is not supported, so upgrade them together.

Gas is pre-1.0: minor versions may contain breaking changes. See the [CHANGELOG](CHANGELOG.md).

## Documentation

| Where | What |
|---|---|
| [gasmod.github.io/gas](https://gasmod.github.io/gas) | Getting started, tutorial, core concepts, and module guides |
| [pkg.go.dev](https://pkg.go.dev/github.com/gasmod/gas) | API reference for every package |

Documentation lives in one place per kind, on purpose. Guides and concepts are on the site, API reference comes
from doc comments on pkg.go.dev, and each module's README is a signpost to both. Each package's `doc.go` is its
behavioral contract: what it promises, its defaults, and its errors, verbatim.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow: conventional
commits, signed-off commits (DCO), tests with every change, and `make lint`.

```bash
make test-all   # go test -race in every module, inside the Docker test container
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
