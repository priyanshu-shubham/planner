# Set up `planner` for AI agents

You are an AI coding agent (e.g. Claude Code). Follow these steps to wire this
project into the **planner** plan-review loop: you post plans, a human reviews and
comments in a browser.

## 1. Install the CLI (if missing)

Run `planner help`. If it prints usage, skip to step 2. If the command is missing,
**you** install it — download it from the server:

macOS / Linux:

```sh
mkdir -p ~/.local/bin
curl -fLo ~/.local/bin/planner --compressed "{{BASE}}/cli/$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')"
chmod +x ~/.local/bin/planner
```

Then make sure `~/.local/bin` is on PATH for future shells (add
`export PATH="$HOME/.local/bin:$PATH"` to the shell profile if needed).

Windows (PowerShell — use `windows-arm64` on ARM machines):

```powershell
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\planner" | Out-Null
Invoke-WebRequest "{{BASE}}/cli/windows-amd64" -OutFile "$env:LOCALAPPDATA\planner\planner.exe"
```

Then add that directory to PATH:
`[Environment]::SetEnvironmentVariable("Path", $env:Path + ";$env:LOCALAPPDATA\planner", "User")`.

If the download returns a 404, this server was built without CLI binaries — ask
the human to install the CLI instead.

One more prerequisite: the **server** runs at **{{BASE}}**, managed by the human —
**never start it yourself**. If a command can't reach the server, ask the human to
start it, then retry.

## 2. Point the CLI at this server

{{IF_NOAUTH}}
This server needs no login. **You** run this once — it records the server (so later
commands know where to talk) and exits:

```sh
planner setup --server {{BASE}}
```
{{END_NOAUTH}}
{{IF_AUTH}}
This server **requires a Google login**, which only a human can complete in a
browser. Run setup **in the background** so you can read the URL it prints while it
keeps waiting:

```sh
planner setup --server {{BASE}} &
```

It prints an **authorization URL** and listens for the result (it does not pop a
browser when you run it). **Give that URL to the human** and ask them to open it,
sign in with Google, and name this machine. The token is then handed back to the
waiting `planner setup` automatically, which prints `authenticated as <email>` and
exits. Wait for that before continuing — after it the CLI is authorized and you run
`planner` commands normally.

**Use the command above (no `--no-browser`).** It runs a local listener that the
browser redirects back to, so the token is captured automatically — you never paste
anything. Only add `--no-browser` if the human's browser runs on a *different
machine* than this CLI (e.g. you're on a remote/SSH box), where that local listener
isn't reachable; then the human pastes the token shown on the page.
{{END_AUTH}}

## 3. Add the usage section to your global `CLAUDE.md`

Append the block below to your **global `~/.claude/CLAUDE.md`** (create it if
needed) — the user-level memory, **not** the project's local `CLAUDE.md`. Replace
any existing `<!-- BEGIN PLANNER -->` section; leave everything else untouched. If a
`<!-- BEGIN PLANNER -->` block already exists, do **not** assume it is current —
replace it wholesale with the block below even if it looks similar; older versions
differ in subtle but important ways.

<!-- BEGIN PLANNER -->
## Plan review with `planner`

Before any non-trivial change, develop a plan in plan mode, post it to `planner`,
and iterate with the human until they approve. Only then start implementing. Every
substantive `planner` create *and* update goes through plan mode — post from its
temp file, never hand-author a plan and post it directly. Very small edits (typos,
a line of wording) can be posted directly without entering plan mode.

**Never run `planner serve` or `planner setup`** — the human runs the server, and
setup is a one-time step already done. The CLI is pointed at the server and signed
in (if it requires a login), so run the commands below directly.
If a command can't reach the server or reports that authentication is required, ask
the human to open the planner site and use **AI setup** again — don't try to fix it
yourself.

**A good plan** is reviewable and actionable: goal/context (1–3 sentences); an
ordered task checklist (`- [ ] …`) small enough to verify each step; key files and
design trade-offs; verification (tests/commands and expected results); risks and
what's out of scope.

**Reference files in the plan by their repo-relative path** so planner automatically
shows their preview to the human — write `internal/web/server.go`, or
with a line range `internal/web/server.go:120-140`, rather than an absolute path or
a bare filename. Paths outside the repo (or absolute ones) render as plain text.

**The loop:**
1. **Post** the plan from plan mode's temp file — don't write a `plan.md` into the
   project: `planner create --title "<title>" --file <plan-mode-file>` (or pipe via
   stdin). Give the human the printed URL to review.
2. **Read feedback:** `planner comments <plan-id> --json` — each comment has `body`,
   the `quote`/lines it anchors to (or `whole_file`), and `replies`.
3. **Respond:** reply to address, ask, or push back —
   `planner reply <plan-id> <comment-id> -m "..."` (attributed to the agent). When you revise,
   **while still in plan mode** (re-enter it if you've since left), edit the
   plan-mode temp file there, then post a new version:
   `planner update <plan-id> --file <plan-mode-file>`.
4. **Repeat** 2–3 until approved. Open comments are blocking — don't code while any
   on the latest version are unaddressed. You don't resolve comments; the human does.

Other: `planner show <plan-id>` prints the latest version (`--version N` for one).
<!-- END PLANNER -->

## 4. Confirm

Tell the user planner is set up, and that you'll post plans to {{BASE}} for review
before implementing.
