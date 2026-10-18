# Contributing

This is the single source of project rules, for humans and AI agents alike.

## Setup

1. Install Go 1.27+ (latest stable) and [Task](https://taskfile.dev)
   (`brew install go-task`, `winget install Task.Task`, `scoop install task`
   or `go install github.com/go-task/task/v3/cmd/task@latest`).
2. Clone, then once per clone:

   ```
   task hooks          # git hooks: gofmt, go vet, commit message check
   cp .env.example .env
   ```

3. Fill in `.env`. Use a **separate dev bot** from @BotFather: two processes
   polling the same token get `409 Conflict`. Put your own Telegram id in
   `ADMIN_IDS` (ask @userinfobot) and send `/start` to the bot.
4. `task dev` runs with hot reload. The bot doesn't search on start; use
   `/fetch` in the chat to trigger a search.

VS Code will suggest the Go, Task and SQLite Viewer extensions
(`.vscode/extensions.json`).

## Commands

```
task test     # go vet + go test — run before pushing
task build    # static binary into bin/
task dev      # hot reload (air), needs a real .env
task          # list all tasks
```

Without Task: `go vet ./... && go test ./...`. `gofmt -l .` must print nothing.

## Project layout

Telegram bot that finds Dead by Daylight promo codes via an LLM with web
search, lets admins moderate them and broadcasts approved codes. Promo codes
are the first feature; more (e.g. drops) will follow, so keep features
separable.

Packages under `internal/`, entry point in `cmd/`; config files stay in the
root:

| Package | Responsibility |
|---------|----------------|
| `cmd/fogwatch` | env config, wiring, cron schedule |
| `internal/bot` | update handling, commands, moderation, broadcast, fetch job, long-poll loop |
| `internal/store` | GORM models and all DB access (SQLite) |
| `internal/ai` | Responses API client (OpenAI / OpenRouter), web search, JSON schema |
| `internal/telegram` | minimal hand-written Telegram Bot API client |
| `internal/i18n` | all user-facing texts (ru, en) and command menus |

Dependencies point one way: `bot` uses `store`, `ai`, `telegram` and `i18n`;
those don't import `bot` or each other (`i18n` only uses `telegram.Cmd` for
menus). Tests sit next to the code as `*_test.go`.

## Rules

### Dependencies
- Standard library first. Current deps: GORM + `ncruces/go-sqlite3` (pure
  Go), `robfig/cron/v3`. Discuss any new module before adding it.
- No Telegram or OpenAI SDKs: extend `internal/telegram` /
  `internal/ai`.
- `go.mod` targets Go 1.27; CI, Docker and local builds use the same minor.
  Bump the `go` line, the Dockerfile base image and deps together, in their
  own `build:` PR — never as part of an unrelated change.
- The binary must build with `CGO_ENABLED=0`.

### Database
- Schema is applied by GORM `AutoMigrate` at startup, which only adds things:
  new tables, nullable columns, columns with a default.
- New `NOT NULL` columns need a `default`, or startup fails on existing data.
- Renames, drops and data migrations are manual: put the SQL in the PR
  description; don't encode them in Go.
- All queries live in `internal/store`; no other package touches `*gorm.DB`.
- State changes that can race (e.g. moderation) use conditional updates
  (`UPDATE … WHERE status = ?`) and check `RowsAffected`.

### Telegram
- Messages use `parse_mode: HTML`: escape every dynamic value with
  `html.EscapeString`.
- `callback_data` ≤ 64 bytes. Prefixes in use: `l:` (language), `ok:`/`no:`
  (moderation). Admin-only callbacks re-check `b.admins`.
- One code per message with a `copy_text` button; users delete the message to
  mark a code as used. No per-user code state — keep it that way.
- Never log the bot token (`internal/telegram` strips URLs from
  errors).

### Texts
- Every user-facing string goes through `i18n.T(lang, key)` and exists in
  both `ru` and `en` in `internal/i18n`. Command menus live there too.

### Behaviour to preserve
- No LLM call on startup; search runs on the cron and via admin `/fetch`.
- Scheduled search failures alert admins once (after 2 in a row) and once on
  recovery — never on every failed run.
- Search results stay `pending` until an admin approves; admin-added codes are
  published at once; `removed` is a tombstone that search can't undo.
- LLM output is validated (code format, source URL, expiry), never trusted.

### Tests
- SQLite in `t.TempDir()`, HTTP via `httptest`; no real network calls.
- Cover store logic and anything that parses external input.

### Secrets
- Never commit `.env`; never log tokens or keys.

## Workflow

- Branch from `main` as `<type>/<short-kebab-description>`, using the commit
  types below: `feat/drops-tracking`, `fix/empty-expiry`, `chore/bump-deps`.
  Not enforced; branches are deleted after merge.
- Open a PR into `main`. PRs are **squash-merged**.
- Commit messages and the **PR title** follow the format below. The PR title
  becomes the commit on `main` and drives versioning.
- CI checks formatting, vet, tests, the Docker build and the PR title.
- Gemini reviews each PR when it's opened or marked ready (comments only, not
  blocking). Treat its comments as suggestions; resolve or reply to them.
- Releases are automatic: release-please opens a release PR; merging it tags
  a version and publishes Docker images. Don't edit `CHANGELOG.md` or the
  release-please manifest by hand.

### Commit messages

[Conventional Commits](https://www.conventionalcommits.org) without scopes:

```
<type>[!]: <description>
```

- English, imperative, lowercase start, no trailing period:
  `feat: add drops tracking`, `fix: ignore codes without source url`.
- **No scopes**: `fix(store): …` is rejected by the commit hook and the PR
  title check.
- `!` after the type marks a breaking change: `feat!: drop /all command`.

| Type | Use for | Release |
|------|---------|---------|
| `feat` | new user- or admin-visible functionality | minor (patch while 0.x) |
| `fix` | bug fix | patch |
| `perf` | performance improvement | patch |
| `revert` | reverting an earlier commit | patch |
| `refactor` | code change without behaviour change | — |
| `test` | tests only | — |
| `docs` | documentation only | — |
| `style` | formatting only | — |
| `build` | build, Dockerfile, Go/module versions | — |
| `ci` | GitHub Actions, hooks | — |
| `chore` | everything else (deps, tooling, housekeeping) | — |
| any type + `!` | breaking change | major (minor while 0.x) |

"—" means: no release on its own and not shown in the changelog; it ships with
the next release triggered by another commit. Also accepted locally (never
reach `main` with squash merge): `fixup! …`, `squash! …`, `Merge …`,
`Revert …`.
