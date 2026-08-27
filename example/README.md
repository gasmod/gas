# Gas examples

Part of the [Gas](../README.md) monorepo · [All modules](../README.md#modules)

Five runnable applications, ordered by how much of the framework they use. Each is its own Go module and each
has a `Makefile` with `test`, `build`, `lint`, `fmt`, and `vet` targets.

| Example                                    | Runs with          | What it shows                                                                  |
|--------------------------------------------|--------------------|--------------------------------------------------------------------------------|
| [hello-world](hello-world/)                | nothing            | The smallest possible app: one inline handler, no services, no config          |
| [hello-world-2](hello-world-2/)            | nothing            | Service lifetimes, route groups, binding and validation, custom error handling  |
| [templates-basic](templates-basic/)        | nothing            | Server-rendered HTML with `gas/ui`: layouts, partials, static files             |
| [lambda-worker](lambda-worker/)            | Postgres, SQS      | `gas.Worker` instead of `gas.App`, for AWS Lambda and background jobs           |
| [api-server](api-server/)                  | Postgres, LocalStack | A real API: auth, database, storage, queue, email, and cache together         |

## hello-world

An entire Gas app in about ten lines. Start here to see the shape of `NewApp` → `Router().Handle` → `Run`.

```bash
cd hello-world
go run ./cmd     # http://localhost:8080
```

## hello-world-2

The framework tour, still with no external dependencies. Loads config from `config.json` and demonstrates:

- All three DI lifetimes, singleton, scoped, and transient, side by side
- Modules that register their own routes, including nested `Route`/`Group` blocks with scoped middleware
- `BindJSON` with validation, and what the error responses look like
- A custom `ErrorHandler`, plus panic recovery and `http.ErrAbortHandler` behaviour
- A ready hook and per-request logging with a request ID

```bash
cd hello-world-2
go run ./cmd
```

Routes worth poking at: `/`, `/greet/{name}`, `/json`, `/error`, `/panic`, `/abort`, and the `/notes` group.

## templates-basic

Server-rendered HTML using [gas/ui](../ui/README.md) over a filesystem
[template store](../template/README.md), with static assets served from `static/`.

```bash
cd templates-basic
go run ./cmd     # http://localhost:8080
```

Config lives in `config.json`. Because `GasEnv` is `development`, templates are rebuilt on every request, so
edits under `templates/` show up on refresh.

## lambda-worker

The same DI container, service lifecycle, and migrations as `gas.App`, without a router or HTTP server. The
worker is built in `init()` so it survives across Lambda invocations, and `Shutdown` is wired to Lambda's
SIGTERM callback.

Needs a Postgres database and an SQS queue, configured through `APP_`-prefixed environment variables:

```bash
cd lambda-worker
export APP_DATABASE__DSN='postgres://user:pass@localhost:5432/orders?sslmode=disable'
export APP_QUEUE__REGION=us-east-1
go run ./cmd
```

## api-server

A file-vault API that puts most of the framework to work at once: JWT and API key authentication, a Postgres
database with sqlc-generated queries, S3 storage, an SQS queue, SES email, and an in-memory cache.

Dependencies run under Docker Compose (Postgres plus LocalStack for S3, SQS, and SES):

```bash
cd api-server
docker compose up -d
cp .env.example .env
go run ./cmd
```

Endpoints:

| Method   | Path                          | Notes                          |
|----------|-------------------------------|--------------------------------|
| `POST`   | `/api/auth/register`          | Public                         |
| `POST`   | `/api/auth/login`             | Public, returns a JWT          |
| `POST`   | `/api/auth/api-keys`          | Authenticated                  |
| `GET`    | `/api/auth/api-keys`          | Authenticated                  |
| `DELETE` | `/api/auth/api-keys/{id}`     | Authenticated                  |
| `POST`   | `/api/files`                  | Authenticated, uploads to S3   |
| `GET`    | `/api/files`                  | Authenticated                  |
| `GET`    | `/api/files/{id}`             | Authenticated                  |
| `GET`    | `/api/files/{id}/download`    | Authenticated                  |
| `DELETE` | `/api/files/{id}`             | Authenticated                  |
| `POST`   | `/api/files/{id}/share`       | Authenticated, enqueues a notification email |
| `GET`    | `/api/shares/{token}`         | Public, single-use share link  |

## Running the tests

The HTTP examples drive a real server built from `App.Handler()`, so the tests cover the same middleware and
CSRF stack as production. `lambda-worker` tests its handler through the worker's DI container instead. From the
repo root, `make test-all` runs them all along with every module's tests. Per example:

```bash
cd hello-world-2 && make test
```
