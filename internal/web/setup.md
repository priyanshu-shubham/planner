# Set up `planner` for AI agents

You are an AI coding agent (e.g. Claude Code). Follow these steps to wire this
project into the **planner** plan-review loop: you post plans, a human reviews and
comments in a browser.

## 1. Prerequisites

- The **`planner` CLI** must be on your PATH (`planner help` to confirm). If it's
  missing, ask the human to install it.
- The **server** runs at **{{BASE}}**, managed by the human — **never start it
  yourself**. The CLI defaults to `http://localhost:8080`; if {{BASE}} differs, pass
  `--server {{BASE}}` (or set `PLANNER_SERVER={{BASE}}`). If a command can't reach
  the server, ask the human to start it, then retry.

## 2. Add the usage section to your global `CLAUDE.md`

Append the block below to your **global `~/.claude/CLAUDE.md`** (create it if
needed) — the user-level memory, **not** the project's local `CLAUDE.md`. Replace
any existing `<!-- BEGIN PLANNER -->` section; leave everything else untouched.

<!-- BEGIN PLANNER -->
## Plan review with `planner`

Before any non-trivial change, develop a plan in plan mode, post it to `planner`,
and iterate with the human until they approve. Only then start implementing. Every
substantive `planner` create *and* update goes through plan mode — post from its
temp file, never hand-author a plan and post it directly. Very small edits (typos,
a line of wording) can be posted directly without entering plan mode.

**Server:** {{BASE}} (default `http://localhost:8080`). Pass `--server {{BASE}}` if
it differs. The human runs the server — never start it yourself.

**A good plan** is reviewable and actionable: goal/context (1–3 sentences); an
ordered task checklist (`- [ ] …`) small enough to verify each step; key files and
design trade-offs; verification (tests/commands and expected results); risks and
what's out of scope.

**The loop:**
1. **Post** the plan from plan mode's temp file — don't write a `plan.md` into the
   project: `planner create --title "<title>" --file <plan-mode-file>` (or pipe via
   stdin). Give the human the printed URL to review.
2. **Read feedback:** `planner comments <plan-id> --json` — each comment has `body`,
   the `quote`/lines it anchors to (or `whole_file`), and `replies`.
3. **Respond:** reply to address, ask, or push back —
   `planner reply <comment-id> -m "..."` (attributed to the agent). When you revise,
   **while still in plan mode** (re-enter it if you've since left), edit the
   plan-mode temp file there, then post a new version:
   `planner update <plan-id> --file <plan-mode-file>`.
4. **Repeat** 2–3 until approved. Open comments are blocking — don't code while any
   on the latest version are unaddressed. You don't resolve comments; the human does.

Other: `planner show <plan-id>` prints the latest version (`--version N` for one).
<!-- END PLANNER -->

## 3. Confirm

Tell the user planner is set up, and that you'll post plans to {{BASE}} for review
before implementing.
