# Deploying planner (Postgres backend)

planner runs as a single container backed by Postgres. SQLite stays the default
for local use; Postgres is selected with `--backend postgres` (the Docker image
defaults to it via `PLANNER_BACKEND=postgres`).

## Prerequisites

- A reachable Postgres database (managed or self-hosted). Any recent version
  works; the server creates its own tables and indexes on first start, so no
  manual migration is needed.
- A connection string (DSN), e.g.
  `postgres://USER:PASSWORD@HOST:5432/DBNAME?sslmode=require`.

## Configuration

The server reads two settings:

- `PLANNER_BACKEND=postgres` — already set in the image; the env var below makes
  it explicit.
- `--db <DSN>` or `$PLANNER_DB` — the Postgres connection string. The `--db` flag
  doubles as the DSN when the backend is postgres; the server refuses to start on
  the postgres backend without one.

`$PORT`, if set, overrides the listen port (so the container fits platforms that
inject it); otherwise the server binds `:8080`.

## Run it

With Docker (or any container platform):

```sh
docker build -t planner .
docker run -p 8080:8080 \
  -e PLANNER_BACKEND=postgres \
  -e PLANNER_DB='postgres://USER:PASSWORD@HOST:5432/DBNAME?sslmode=require' \
  planner
```

Or run the binary directly:

```sh
planner serve --backend postgres --db 'postgres://USER:PASSWORD@HOST:5432/DBNAME?sslmode=require'
```

The startup banner prints the backend and a password-redacted DSN.

## ⚠️ Authentication / exposure

planner has **no authentication of its own**. Exposing it publicly would reveal
every plan and comment to anyone with the URL. Put it behind one of:

- an authenticating reverse proxy / load balancer, or
- a private network reachable only by trusted clients.

Adding an in-app auth layer is intentionally **out of scope**.

## Local development

Nothing here changes local use — `planner serve` still defaults to SQLite:

```sh
planner serve                 # sqlite at ~/.planner/planner.db
planner serve --backend postgres --db <DSN>   # talk to a real Postgres
```

To run the backend-conformance tests against Postgres:

```sh
docker run --rm -e POSTGRES_PASSWORD=pw -p 5432:5432 postgres:16   # in one shell
export PLANNER_TEST_POSTGRES_DSN='postgres://postgres:pw@localhost:5432/postgres?sslmode=disable'
go test ./internal/store                                            # runs sqlite + postgres
```
