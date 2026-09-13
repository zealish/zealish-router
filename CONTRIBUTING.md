# Contributing

Thanks for helping out. This document covers the local workflow and the
conventions the codebase follows.

## Getting started

Requires Go 1.25. No cgo toolchain is needed — the SQLite driver is pure Go and
everything builds with `CGO_ENABLED=0`.

```sh
git clone https://github.com/zealish/zealish-router
cd zealish-router
make build
make test
```

## Before opening a pull request

```sh
make fmt     # gofmt -l -w .
make vet     # go vet ./...
make lint    # golangci-lint run ./...
make race    # go test -race ./...
```

CI runs the same checks. A pull request that fails `gofmt`, `go vet`,
`golangci-lint` or the race detector will not be merged.

`golangci-lint` is not vendored; install it separately or run it through
`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...`.

## Dashboard

`apps/dashboard` is a separate Next.js app requiring Node 24. It talks only to
the admin API at `/api/v1`; no model traffic passes through it.

```sh
cd apps/dashboard
npm install
npm run dev      # http://localhost:3000, expects the router on :8787
npx eslint .     # both run in CI
npm run build
```

`PROVIDER_KINDS` in `src/lib/api.ts` mirrors the kinds registered in
`internal/router/loader.go` — extend both when adding a provider.

## Project layout

```
cmd/server        entrypoint, CLI, dependency wiring
internal/api      HTTP transport: gateway, admin, middleware
internal/router   alias resolution, fallback chain, retry
internal/provider provider implementations and upstream error mapping
internal/stream   SSE writer and relay
internal/storage  SQLite and in-memory stores, migrations
internal/auth     key hashing, generation, authentication
internal/config   YAML loading, validation, live reload
internal/metrics  private Prometheus registry
pkg/openai        OpenAI wire types
apps/dashboard    Next.js dashboard (Overview, Models, Providers, Keys, Settings)
```

Dependencies point inward. `internal/config` imports nothing from the project;
`internal/api` translates HTTP to domain calls and holds no routing logic of its
own. Everything is injected explicitly through `api.Dependencies` — there is no
global state, including the Prometheus registry.

## Conventions

- **Comments explain why, not what.** Skip a comment when the code already says
  it; write one where a decision is non-obvious (a fallback rule, a timing
  guarantee, why a lock is not held).
- **Errors wrap.** Use `%w` and compare with `errors.Is` / `errors.As`. Upstream
  failures are classified into the sentinels in `internal/provider/errors.go`.
- **Never leak secrets.** API keys are stored as SHA-256 hashes, never
  serialised back over the admin API, and stripped from provider error messages
  by `provider.redact`. Client-facing errors carry no internal detail.
- **Tests use the standard library.** `testing` plus `httptest`; no assertion
  framework. Table-driven tests where the cases are uniform.
- **No network in tests.** Providers are exercised against `httptest` servers.

## Adding a provider

1. Add the implementation in `internal/provider`, embedding `httpProvider` for
   the shared request/error handling. See `openrouter.go` for the minimal case:
   it only contributes extra headers.
2. Register the `kind` in the `buildRegistry` switch in
   `internal/router/loader.go`. An unknown kind falls back to plain OpenAI.
3. Add a test that covers the non-streaming path, the streaming path and error
   mapping, following `internal/provider/http_test.go`.
4. Add the kind to `PROVIDER_KINDS` in `apps/dashboard/src/lib/api.ts` so it is
   selectable in the dashboard.

## Database changes

Migrations are embedded SQL files in `internal/storage/migrations`, applied in
filename order and tracked in `schema_migrations`. Add a new numbered file;
never edit one that has shipped.

## Commits

Write an imperative subject line under ~72 characters, and use the body to
explain the reasoning behind a non-trivial change.
