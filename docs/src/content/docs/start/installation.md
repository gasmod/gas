---
title: Installation
description: Add Gas to a Go project, register modules and run your first app.
---

## Requirements

- Go 1.27.3 or newer.

That is all the core needs. Some modules talk to outside services, such as
PostgreSQL, Valkey or AWS, and only need them when you use those backends.

## Add the core

In your Go module:

```sh
go get github.com/gasmod/gas
```

The core is enough for a working HTTP app. See the example on the
[home page](/gas/).

## Add modules

Each module is its own Go module. Add only the ones you use:

```sh
go get github.com/gasmod/gas/config
go get github.com/gasmod/gas/log
go get github.com/gasmod/gas/database
```

All modules share one version and are released together. Keep them on the
same version as the core.

## Register modules

Modules are services. You register them with the App, and its container
builds them, wires their dependencies and runs their lifecycle. This app
reads its settings from environment variables, logs through slog, and opens
a SQLite database:

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
	_ "modernc.org/sqlite"
)

func main() {
	cfg := config.New(config.WithProvider(
		config.NewEnvProvider(config.WithEnvPrefix("APP_")),
	))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
	)

	app.Router().Handle(nil, http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})

	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

A few things to know:

- **Configuration needs a prefix.** The environment provider only reads
  variables that start with the prefix you give it, here `APP_`. Other
  tools' variables, such as their API keys, stay out of your config.
- **Nested keys use `__`.** `APP_DATABASE__DSN` sets the key
  `database.dsn`.
- **SQLite needs a driver.** Import `modernc.org/sqlite`, or another driver
  registered as `sqlite`. PostgreSQL and MySQL drivers come with the
  database module.
- **Modules log through a `gas.Logger`.** Register one, as above. The
  container resolves dependencies, so the order of registrations does not
  matter.

## Run it

```sh
go get modernc.org/sqlite
APP_DATABASE__DIALECT=sqlite APP_DATABASE__DSN=file:app.db go run .
```

```
INFO service initialized service=gas/database mode=sql dialect=sqlite
INFO server started environment=development addr=0.0.0.0:8080
```

`curl localhost:8080` answers `Hello, World!`.

## Next steps

- [Core](/gas/reference/gas/): services, routing, handlers and middleware.
- [config](/gas/reference/config/): every provider and how values bind to
  structs.
- [database](/gas/reference/database/): dialects, connection settings and
  transactions.
