# AGENTS.md

Project rules, layout and commands are in CONTRIBUTING.md. Read it and follow
it before making changes:

@./CONTRIBUTING.md

## Agent-specific notes

- Run `task test` (or `go vet ./... && go test ./...`) and `gofmt -l .` before
  reporting a change as done.
- Don't run `task dev`, `task start` or the bot itself: they need real tokens
  and talk to Telegram and the LLM API.
- Ask before adding a dependency, bumping the Go version or upgrading modules.
- If a change needs a manual DB migration (rename, drop, data fix), stop and
  give the SQL instead of working around it in code.
