# Deploying planner (Postgres backend)

planner runs as a single container backed by Postgres. SQLite stays the default
for local use; Postgres is selected with `--backend postgres` (the Docker image
defaults to it via `PLANNER_BACKEND=postgres`).

## One-shot deploy (GKE Knative + Cloud SQL)

`deploy/deploy.sh` does the whole thing from one env file: provisions the database
and user in a Cloud SQL (Postgres) instance, builds and pushes the image to Artifact
Registry, and applies a Knative Serving `Service` to your current `kubectl` context.

```sh
cp deploy/planner.env.example deploy/planner.env   # gitignored — holds the password
$EDITOR deploy/planner.env
kubectl config use-context <your-gke-context>       # the deployer targets the current context
./deploy/deploy.sh                                  # or --dry-run first
```

The script is idempotent — re-run it to ship a new version. Phases can run in
isolation:

```sh
./deploy/deploy.sh --dry-run        # print the rendered manifest + commands, change nothing
./deploy/deploy.sh --skip-build --skip-deploy   # just create the DB + user
./deploy/deploy.sh --skip-db --skip-deploy      # just build + push the image
./deploy/deploy.sh --skip-db --skip-build       # just (re)apply the Knative Service
./deploy/deploy.sh --env path/to/other.env      # use a different env file
```

Notes:
- **DB provisioning needs no DB connectivity.** It uses the Cloud SQL Admin API
  (`gcloud sql databases/users create`), so it works against a private-IP-only
  instance from your laptop. Cloud SQL auto-grants `cloudsqlsuperuser` to the user
  it creates, so planner can create its own tables on first boot — no GRANT or
  migration step. An existing user keeps its current password.
- **Connectivity is Private IP direct.** The Service's DSN points at the instance's
  private IP (auto-derived if `DB_HOST` is blank); the GKE pods reach it over the
  cluster VPC. The DSN (with the password) is stored in a `${SERVICE_NAME}-db`
  Secret.
- **Builds target `linux/amd64`** (`docker build --platform linux/amd64`) so an
  Apple-Silicon build still runs on amd64 GKE nodes — needs Docker buildx/qemu
  (Docker Desktop ships it). Docker is assumed already authenticated to Artifact
  Registry.
- **Knative must already be installed** on the target cluster; the script checks for
  the `services.serving.knative.dev` CRD and errors clearly if it's missing (it does
  not install Knative).
- **Auth is optional.** Set `PLANNER_AUTH=1` (plus `GOOGLE_CLIENT_ID` /
  `GOOGLE_CLIENT_SECRET`, and ideally `PLANNER_AUTH_SECRET`) in the env file to enable
  Google login; those land in a `${SERVICE_NAME}-auth` Secret. Auth additionally needs
  a stable HTTPS host with the `https://YOUR_HOST/auth/google/callback` redirect
  registered — that DNS/TLS wiring is **out of scope** for this script (see the
  Authentication section below).

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

## CLI distribution

The image ships cross-compiled `planner` CLI binaries (linux/darwin/windows ×
amd64/arm64, built by `make cli-dist`, stored gzipped — ~38 MB total instead of
~106 MB raw) in `/opt/planner/cli` and sets `PLANNER_CLI_DIR` to point at it. The
server serves them publicly at `GET /cli/{os}-{arch}`: clients that accept gzip
(e.g. `curl --compressed`) get the compressed bytes, anyone else gets them
decompressed on the fly, so the install command always yields a working binary.
The agent-facing `/setup.md` tells agents to install the CLI from there — every
machine gets a CLI that exactly matches the server's version. Outside the image,
set `$PLANNER_CLI_DIR` to a `make cli-dist` output directory to enable the
endpoint (plain uncompressed binaries in that directory work too); without it the
endpoint returns a friendly 404 and setup falls back to a human-installed CLI.

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

## Authentication

planner ships an **optional** in-app auth layer. It is **off by default**: a plain
`planner serve` (or the Docker image as shipped) has no authentication and exposes
every plan to anyone who can reach it — so an unauthenticated instance still needs
a private network or an authenticating proxy in front of it.

### Authenticated mode

Start the server with `--auth` (or `$PLANNER_AUTH=1`) to require a Google login and
scope every plan to the user who created it. Web users sign in with Google; the
CLI authorizes a machine via `planner setup` (a browser handoff that mints a
personal access token). It needs:

- `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` — an OAuth 2.0 *Web application*
  client from the [Google Cloud console](https://console.cloud.google.com/apis/credentials).
  Register the redirect URI `https://YOUR_HOST/auth/google/callback` (use
  `http://localhost:8080/auth/google/callback` for local testing).
- `PLANNER_AUTH_SECRET` *(optional)* — the HMAC key used to sign access tokens. If
  unset, a random key is generated per start: access tokens then become invalid on
  restart, but browsers recover transparently via the refresh cookie, so the only
  effect is that the CLI/SPA mint a fresh access token after a restart. Pin it to a
  stable secret to avoid that, and **required** if you run more than one instance
  (all instances must share the same key).

```sh
docker run -p 8080:8080 \
  -e PLANNER_BACKEND=postgres \
  -e PLANNER_DB='postgres://USER:PASSWORD@HOST:5432/DBNAME?sslmode=require' \
  -e PLANNER_AUTH=1 \
  -e GOOGLE_CLIENT_ID='…apps.googleusercontent.com' \
  -e GOOGLE_CLIENT_SECRET='…' \
  -e PLANNER_AUTH_SECRET="$(openssl rand -hex 32)" \
  planner
```

The server must be reached over HTTPS in production (cookies are marked `Secure`
when the request is HTTPS, directly or via an `X-Forwarded-Proto: https` proxy).

### ⚠️ Mode downgrade exposes existing plans

Auth is enforced only while `--auth` is on. Plans created in authed mode carry an
owner; plans created **before** auth was enabled (or while it was off) have no
owner and are **invisible** in authed mode. Conversely, **restarting an authed
server without `--auth` turns filtering off and exposes every plan to every
client.** Don't flip an authed instance back to no-auth on a public network.

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
