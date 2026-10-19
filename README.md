<p align="center">
  <img src="assets/avatar.svg" alt="Fogwatch" width="160">
</p>

# Fogwatch

Telegram bot that collects Dead by Daylight promo codes via LLM web search
(OpenAI or OpenRouter), lets an admin approve them, and sends each approved
code once to everyone who pressed /start.

Try it: [@fogwatch_bot](https://t.me/fogwatch_bot).

## Run

Needs a bot token from [@BotFather](https://t.me/BotFather) and an OpenAI or
OpenRouter API key. Put the variables from [Configuration](#configuration)
into `.env` (template: [.env.example](.env.example)) next to `compose.yaml`:

```yaml
services:
  fogwatch:
    image: ghcr.io/evlmaistrenko/fogwatch:latest
    env_file: .env
    volumes:
      - ./data:/data
    restart: unless-stopped
```

The container runs as uid 65532 (distroless `nonroot`), so create `data/`
with that owner first, or Docker creates it as root and the bot can't write:

    mkdir -p data && sudo chown 65532:65532 data
    docker compose up -d                            # start
    docker compose pull && docker compose up -d     # update

The image is also on Docker Hub as `evlmaistrenko/fogwatch` (linux/amd64,
linux/arm64). `latest` is the last release; pin `X.Y` or `X.Y.Z` to control
updates; `main` follows every push to `main`. The version is logged at start.

Admins get their extended command menu after pressing /start in the bot; no
restart needed.

## Configuration

Environment variables (see [.env.example](.env.example)):

| Variable | Default | |
|----------|---------|---|
| `TELEGRAM_TOKEN` | — | bot token, required |
| `ADMIN_IDS` | — | admin Telegram user ids, comma-separated ([@userinfobot](https://t.me/userinfobot)) |
| `OPENAI_API_KEY` | — | required |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | any Responses API, e.g. `https://openrouter.ai/api/v1` |
| `OPENAI_MODEL` | `gpt-5-mini` | `openai/gpt-5-mini` on OpenRouter |
| `TZ` | `UTC` | "today" for code expiry, search schedule, log timestamps |

## Commands

    /start    help + recent codes, one message each (without an expiry
              date: added within the last 7 days)
    /codes    all active codes, one message with pages and copy buttons:
              expiring soonest, then recent, then older without a date;
              admins see the whole database with status filter buttons
              (🎁 active, ⏳ pending, 🚫 rejected, ⌛ expired, 🗑 removed)
    /lang     ru / en / same as Telegram (default)

Admin (shown in the admin's menu):

    /pending                          resend moderation cards
    /add CODE [YYYY-MM-DD] [reward]   publish + broadcast now;
                                      "ru text | en text" sets both languages
    /del CODE                         tombstone: search won't re-add it
    /fetch                            search now

## Behaviour

- Search runs every 3 hours (00:00, 03:00, … in `TZ`), never on start. The
  prompt lists the 200 most recent known codes (any status) so the model looks
  for new ones only.
- If 2 scheduled searches in a row fail, admins get one alert (the error
  itself only goes to the log), and one "works again" message after the next
  success.
- Moderation: a new code from search is stored as pending and sent only to
  admins with ✅ Publish / ❌ Reject. Publish broadcasts it; reject keeps a
  tombstone so search won't bring it back. With several admins the first
  click wins. Admins must have pressed /start to receive cards.
- A published code is sent once to every user, as its own message with a 📋
  copy button. No per-user state: users track codes with Telegram itself
  (delete redeemed ones, react, pin, …).
  Users who blocked the bot are removed on the next send.
- Flood control (429) is retried with Telegram's retry_after.
- LLM: Responses API at `OPENAI_BASE_URL`. Web search uses OpenAI's
  `web_search` tool, or `openrouter:web_search` when the URL is OpenRouter
  (any model; Exa fallback for models without native search, billed extra).
- AI results are filtered: code must match `[A-Z0-9-]{4,30}`, have a source
  URL, and not be past its expiry. Codes without a known expiry stay active
  until an admin removes them, but `/start` only sends the ones added within
  the last 7 days; `/codes` lists them all.

## Storage

Users and codes live in SQLite at `data/db.sqlite` (relative to the working
dir; `./data` in the Compose example). The schema is created and extended
automatically at start. WAL mode: back up all `db.sqlite*` files with the bot
stopped, or use `sqlite3 data/db.sqlite ".backup backup.sqlite"`.

## Development

Setup, commands, project rules, CI and releases:
[CONTRIBUTING.md](CONTRIBUTING.md).
