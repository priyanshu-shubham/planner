# planner

A plan-review loop between an AI coding agent and a human. The agent posts a plan;
the human reviews it in a browser, selecting text to leave inline comments; the
agent reads the feedback, replies or revises, and posts new versions until the
human approves. Only then does the agent start implementing.

planner is a single Go binary: a small JSON API serving both a React web UI (the
human's interface) and the `planner` CLI (the agent's interface). The server owns
the database — SQLite by default, Postgres for deployments.

## Quick start

```sh
planner serve                 # serves on http://localhost:8080 (SQLite at ~/.planner/planner.db)
```

Open http://localhost:8080 to review plans. Point an agent at the server by pasting
this to it (Claude Code et al.):

```
Follow http://localhost:8080/setup.md and set up planner
```

That instructs the agent to run `planner setup` once and adds a planner usage
section to its global `CLAUDE.md`.

## CLI

```
planner setup    --server URL [--name NAME] [--no-browser]   # point the CLI at a server (once)
planner create   --title TITLE [--file plan.md]              # post a new plan (reads stdin if no --file)
planner update   PLAN_ID [--file plan.md]                    # post a new version
planner show     PLAN_ID [--version N] [--json]
planner comments PLAN_ID [--version N] [--status open|all] [--json]
planner reply    COMMENT_ID [-m MESSAGE]
```

`planner setup` records the server in `~/.planner/config.json`, so client commands
afterward need no `--server`. The server resolves as `$PLANNER_SERVER` >
`~/.planner/config.json` > `http://localhost:8080`.

A deployed server (the Docker image) also distributes its own CLI at
`/cli/{os}-{arch}`, version-matched to the server — e.g.:

```sh
curl -fLo ~/.local/bin/planner --compressed "https://YOUR_HOST/cli/$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')"
chmod +x ~/.local/bin/planner
```

(Platforms: `linux`, `darwin`, `windows` × `amd64`, `arm64`; Windows files end in
`.exe`. A locally run `planner serve` returns 404 here unless `$PLANNER_CLI_DIR`
points at a `make cli-dist` output directory.)

## Authentication (optional)

By default planner has no authentication and is single-user. Run the server with
`--auth` (needs `$GOOGLE_CLIENT_ID` and `$GOOGLE_CLIENT_SECRET`) to require a Google
login and make plans private per user. Web users sign in with Google; the CLI is
authorized per machine by `planner setup`, which mints a personal access token via
a browser handoff. Review or revoke authorized machines under **CLI access**
(the account-avatar menu) in the web UI.

```sh
planner serve --auth          # require Google login; per-user plan scoping
```

See [deploy/README.md](deploy/README.md) for running with Postgres and the full
auth setup (Google OAuth client, `PLANNER_AUTH_SECRET`, HTTPS, and the mode-downgrade
warning).

## Development

```sh
cd web/frontend && npm install && npm run build   # rebuild the embedded SPA bundle
go build ./...                                     # build the binary (embeds the bundle)
go test ./...                                      # store + web + cli tests
```

The web UI is built with esbuild into `internal/web/static/`, which the Go binary
embeds — rebuild the bundle after changing anything under `web/frontend/src`.
