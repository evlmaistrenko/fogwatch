# Fogwatch

Telegram bot that collects Dead by Daylight promo codes via LLM web search
(OpenAI or OpenRouter),
lets an admin approve them, and sends each approved code once to everyone
who pressed /start.
Storage: SQLite via GORM (ncruces/go-sqlite3, pure Go, no cgo).

Try it: [@fogwatch_bot](https://t.me/fogwatch_bot).

## Run

    go mod tidy            # once: generates go.sum
    cp .env.example .env   # fill in tokens and ADMIN_IDS
    docker build -t fogwatch .
    docker run -d --name fogwatch --env-file .env -v dbd-data:/data --restart unless-stopped fogwatch

## CI / releases

- PR: gofmt, vet, tests, Docker build (no push) — `.github/workflows/ci.yml`;
  PR title must be a Conventional Commit (squash-merge message) — `pr-title.yml`;
  Gemini posts an AI review (comments only, non-blocking) — `gemini-review.yml`.
- Push to main — `.github/workflows/release.yml`:
  - release-please keeps a release PR up to date from Conventional Commits
    (types and version bumps: CONTRIBUTING.md → Commit messages);
  - every push publishes `main` and `sha-xxxxxxx` images;
  - merging the release PR tags `vX.Y.Z`, creates a GitHub release and
    publishes `X.Y.Z`, `X.Y`, `X` (from 1.0) and `latest`.
- Images (linux/amd64, linux/arm64): `ghcr.io/<owner>/<repo>` and
  `docker.io/<DOCKERHUB_USERNAME>/<repo>`. Version is baked into the binary
  and logged at start.

Repo setup: variable `DOCKERHUB_USERNAME`, secret `DOCKERHUB_TOKEN`
(Docker Hub access token, read/write); secret `GEMINI_API_KEY` (AI Studio),
optional variable `GEMINI_MODEL`; Settings → Actions → General →
"Allow GitHub Actions to create and approve pull requests". `go.sum` must be
committed.

## Dev

Needs Go and [Task](https://taskfile.dev) (`brew install go-task`,
`winget install Task.Task`, `scoop install task` or
`go install github.com/go-task/task/v3/cmd/task@latest`). Works the same on
Linux, macOS and Windows.

    task hooks   # once per clone: lefthook git hooks (gofmt, vet, commit message)
    task dev     # hot reload via air (pinned, fetched on first run)
    task build   # static binary -> bin/
    task start   # build + run
    task test
    task         # list all tasks

`.env` is loaded automatically.

No search on start, so reloads don't cost OpenAI calls; use /fetch to test.
Use a separate dev bot token: two processes
long-polling one token get 409 Conflict.

## Commands

    /start    help + all active codes
    /codes    active codes, one message each
    /lang     ru / en / same as Telegram (default)

Admin (ADMIN_IDS — Telegram user ids from env; shown in the admin's menu):

    /pending                          resend moderation cards
    /all                              everything in the database (⏳ pending, ⌛ expired)
    /add CODE [YYYY-MM-DD] [reward]   publish + broadcast now (or just send the code)
    /del CODE                         tombstone: search won't re-add it
    /fetch                            search now

## Behaviour

- Search runs every 3 hours (00:00, 03:00, … UTC), never on start. The
  prompt lists the 200 most recent known codes (any status) so the model looks
  for new ones only.
- If 2 scheduled searches in a row fail, admins get one alert (the error
  itself only goes to the log), and one "works again" message after the next success.
- Moderation: a new code from search is stored as pending and sent only to
  admins with ✅ Publish / ❌ Reject. Publish broadcasts it; reject keeps a
  tombstone so search won't bring it back. With several admins the first
  click wins. Admins must have pressed /start to receive cards.
- A published code is sent once to every user, as its own message with a 📋 copy
  button. No per-user state: redeemed = delete the message in Telegram.
  Users who blocked the bot are removed on the next send.
- Flood control (429) is retried with Telegram's retry_after.
- TZ (default UTC) only defines "today" for code expiry and log timestamps.
- LLM: Responses API at OPENAI_BASE_URL. Web search uses OpenAI's
  `web_search` tool, or `openrouter:web_search` when the URL is OpenRouter
  (any model; Exa fallback for models without native search, billed extra).
- AI results are filtered: code must match `[A-Z0-9-]{4,30}`, have a source
  URL, and not be past its expiry. Codes without a known expiry stay active
  until an admin removes them.

## Storage

SQLite file `data/db.sqlite` relative to the working dir (`/data` volume in
Docker), WAL mode, schema via GORM AutoMigrate:

    codes       code PK, reward_en, reward_ru, expires, source, url,
                status (pending|published|removed), created_at
    users       id PK (Telegram id), client_lang (follows Telegram),
                lang (explicit /lang choice, '' = follow client), created_at


Contributing and project rules: [CONTRIBUTING.md](CONTRIBUTING.md).
