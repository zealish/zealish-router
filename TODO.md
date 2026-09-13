# TODO — Zealish Router

Checkpoint file. Milestones follow PRD.md §19. Check items off as they land.

Legend: `[x]` done · `[ ]` pending · `~` partial (scaffold only, no logic)

---

## v0.1 — Foundation ✅

- [x] `go.mod` — Go 1.25, chi v5, client_golang, yaml.v3
- [x] Repository layout: `cmd/server`, `internal/*`, `pkg/openai`
- [x] `internal/config` — YAML load + validate (server, database, auth, providers, models, fallback)
- [x] `internal/api` — chi router, `http.Server` lifecycle, graceful shutdown
- [x] `GET /health`
- [x] Middleware: request ID, real IP, recoverer, slog request logger
- [x] `cmd/server/main.go` — flag parsing, DI wiring, signal handling
- [x] `serve` / `validate` commands
- [x] `config.yaml`, `Makefile`, `Dockerfile`
- [x] Builds clean: `go build`, `go vet`, `gofmt`

---

## v0.2 — Chat Completions + Streaming ✅

### Wire format
- [x] `pkg/openai` types — request, response, choice, usage, stream chunk, error envelope
- [x] Support `content` as string **and** array-of-parts — stays `json.RawMessage` for lossless passthrough, with a `Message.Text()` accessor for the string form (resolves Open Decision #5)
- [x] Add `stream_options.include_usage` passthrough (stripped on non-streaming calls)

### Transport
- [x] `POST /v1/chat/completions` handler — decode, validate, dispatch
- [x] `GET /v1/models` — lists configured aliases
- [x] SSE response writing
- [x] Extract SSE into `internal/stream` package (`Writer`, `[DONE]` sentinel, heartbeat/keepalive, generic `Relay`)
- [x] Client-disconnect handling — `Relay` selects on `r.Context()`, cancelling the upstream stream on hangup
- [x] Increment/decrement `router_stream_connections` around stream lifetime
- [x] Set `X-Accel-Buffering: no` for proxy-safe streaming

### Tests
- [x] Handler tests: 400 on malformed JSON, 400 on missing model, 404 on unknown alias
- [x] Handler tests: 502 on upstream 5xx (no detail leak), upstream 4xx passed through
- [x] SSE test: chunks framed correctly, terminated with `data: [DONE]`
- [x] SSE test: headers only commit at first frame, so pre-stream errors still use a status code
- [x] SSE test: client disconnect returns without writing the sentinel
- [x] `stream` unit tests: framing, headers, heartbeat on idle, cancel, flusher requirement

---

## v0.3 — Providers ✅

### Shared HTTP layer
- [x] `internal/provider/http.go` — shared `httpProvider` base (build request, auth header, decode, error mapping)
- [x] SSE parser for upstream responses — line-based, handles `data:` folding, comments/keepalive, stops at `[DONE]`
- [x] Typed upstream errors: `ErrTimeout`, `ErrRateLimited`, `ErrUpstream5xx`, `ErrConnection` + `provider.Error` envelope and `Retryable()` (feeds v0.4 fallback classifier)
- [x] Always close response bodies; propagate `ctx` cancellation into in-flight streams

### Implementations
- [x] OpenAI — `Authorization: Bearer`, `ChatCompletion` + `ChatCompletionStream`
- [x] OpenRouter — same plus `HTTP-Referer` / `X-Title` headers
- [x] Ollama — no API key, longer timeout, OpenAI-compatible `/v1` path
- [x] Remove `ErrNotImplemented` returns from all three

### Tests
- [x] `httptest` server per provider: non-streaming happy path
- [x] `httptest` streaming: multi-chunk SSE decoded into channel, channel closed at `[DONE]`
- [x] Error mapping: 429 → `ErrRateLimited`, 503 → `ErrUpstream5xx`, timeout → `ErrTimeout`, connection refused → `ErrConnection`, 400 → non-retryable

---

## v0.4 — Routing, Fallback, Retry ✅

- [x] Model alias resolution (`Engine.Resolve`)
- [x] Deterministic chain construction (`Engine.Chain`)
- [x] Wire `Chain()` into `ChatCompletion` / `ChatCompletionStream` via generic `dispatch`
- [x] Retryable-error classifier: timeout, rate limit, 5xx, connection error → advance chain; 4xx → return immediately (`provider.Retryable`)
- [x] Bounded retry per provider with exponential backoff + jitter (`Retry` policy, `DefaultRetry` = 2 attempts / 200ms base / 2s cap)
- [x] Streaming fallback rule: only fall back **before** the first chunk is flushed — fallback happens while establishing the channel, committed once returned
- [x] Increment `router_provider_errors_total{provider,reason}` on each failed attempt via injected `ErrorRecorder`
- [x] Log every attempt with alias, provider, attempt index, reason
- [x] Guard against fallback cycles / cap total attempts (dedup + `maxChainAttempts = 8`)

### Tests
- [x] Primary fails 500 → fallback succeeds
- [x] Primary returns 400 → no fallback, error surfaced (and not retried)
- [x] Entire chain exhausted → `ErrChainExhausted` wrapping last error → 502
- [x] Chain order is deterministic across runs
- [x] Retry exhausts per-provider attempts before advancing the chain
- [x] Streaming falls back before the first chunk
- [x] Cancelled client context stops the chain
- [x] Backoff stays within `(0, MaxDelay]`

---

## v0.5 — Persistence, Auth, Metrics ✅

### Storage
- [x] `storage.Store` / `APIKeyStore` interfaces
- [x] In-memory implementation
- [x] **Decided driver**: `modernc.org/sqlite` — cgo would break the `CGO_ENABLED=0` + distroless static build (Open Decision #1)
- [x] `internal/storage/sqlite.go` — implements `Store`
- [x] Schema + embedded migrations: `api_keys`, `providers`, `model_aliases` (PRD §14), tracked in `schema_migrations`
- [x] Auto-create parent dir for `database.path`; WAL mode; busy timeout; single writer conn
- [x] Swap `storage.NewMemory()` for SQLite in `cmd/server/main.go`

### Auth
- [x] SHA-256 hashing, bearer extraction, `Authenticator` interface, context identity
- [x] Static config keys
- [x] Constant-time lookup — `matchStatic` compares every key without early return, so timing does not reveal which (or whether) a key matched
- [x] `TouchLastUsed` on successful authentication (background goroutine, own 3s context, never blocks the response)
- [x] Key generation: `zr_` + 32 base62 chars from `crypto/rand`, returned **once**, only hash persisted

### Metrics
- [x] Private registry, no global state
- [x] `router_requests_total`, `router_request_duration_seconds`
- [x] `router_provider_errors_total`, `router_stream_connections`, `router_tokens_total` declared
- [x] Actually record `router_tokens_total{provider,model,kind}` from `Usage` on both response paths
- [x] Estimate tokens (~4 chars/token) when upstream omits usage, streaming included via `meterStream`

### Tests
- [x] SQLite: migrations idempotent across reopen, WAL enabled, parent dir created
- [x] SQLite: API key CRUD, `ErrNotFound` on unknown id/hash, unique `key_hash`
- [x] Auth: static key, stored key, disabled key, bearer parsing, generated-key shape and uniqueness
- [x] Auth: `last_used_at` recorded after a successful authentication
- [x] Usage: reported usage preferred, estimation fallback, streaming variants of both

---

## v0.6 — Config Reload + CLI ✅

- [x] fsnotify watcher on `config.yaml` (PRD §13 — reload without restart), watching the parent dir so atomic saves survive
- [x] Atomic swap of routing table behind `atomic.Pointer[routes]` — no lock on request path
- [x] Reject invalid reload: log error, keep previous config
- [x] Rebuild provider registry on reload
- [x] `zealish-router keys create --name <name>` — prints the raw key once
- [x] `zealish-router keys list` / `keys revoke <id>`
- [x] `zealish-router models` — print alias → provider/model/fallback table
- [ ] Move to a real subcommand parser if flag handling gets unwieldy

### Tests
- [x] Watcher: valid reload delivered, invalid rejected then recovers on next good write
- [x] Watcher: atomic write-temp-rename replace, burst writes coalesced, cancel stops cleanly
- [x] Engine: reload swaps routes, drops stale aliases, safe under concurrent dispatch (`-race`)

---

## v0.7 — Internal REST API (Dashboard backend) ✅

### Source of truth
- [x] **Decided**: the database is authoritative for providers and model aliases (Open Decision #2). `config.yaml` keeps only process-level concerns: server, database, auth, admin
- [x] Migration `0002_routing.sql` — `providers.kind`/`timeout_ms`, `model_aliases.fallback`, new `settings` table
- [x] `storage.ProviderStore` / `ModelStore` / `SettingStore` — SQLite + in-memory, upsert by name/alias
- [x] `router.Engine` routes off `[]storage.ModelAlias` instead of `*config.Config`; fallback chain now lives on the alias record
- [x] `router.Loader` — rebuilds provider registry + routing table from storage, called after every admin mutation
- [x] Config reload now only refreshes credentials (static keys, admin token); routing is no longer YAML-driven

### Transport
- [x] `internal/api/admin.go` — mounted at `/api/v1`, separate from `/v1`, unmounted entirely when `admin.enabled` is false
- [x] `GET /api/v1/overview` — request counts, error rate, active streams (read from the Prometheus registry via `metrics.Snapshot`), plus resource counts
- [x] `GET|POST|DELETE /api/v1/keys` — raw key returned exactly once on create
- [x] `GET|PUT|DELETE /api/v1/providers` — API keys never serialised back, only `has_api_key`; an omitted `api_key` preserves the stored secret
- [x] `GET|PUT|DELETE /api/v1/models` — validates the referenced provider exists
- [x] `GET|PUT /api/v1/settings`
- [x] CORS for `localhost:3000`, configurable via `admin.cors_origins`; explicit origin echo, never a wildcard, since requests carry credentials
- [x] Admin auth separate from gateway keys — `auth.AdminService`, constant-time token compare, fails closed on an empty token
- [x] `zealish-router models` reads the database instead of the config file

### Tests
- [x] Admin auth: no token → 401, gateway key → 401, admin disabled → 404
- [x] CORS: preflight returns 204 with the configured origin, unlisted origins get no header
- [x] Keys: create/list/delete lifecycle, raw key only at creation, 404 on repeat delete, 400 on missing name / malformed JSON
- [x] Providers: mutation republishes the engine routing table without a restart, secret never leaks, omitted `api_key` preserved on update
- [x] Models: unknown provider → 400, missing fields → 400, delete drops the alias from the engine
- [x] Overview counts providers/models/keys and reports non-zero requests
- [x] Storage: provider/alias/setting round-trip, upsert, fallback encode/decode, ordering, `ErrNotFound`

---

## v1.0 — Dashboard, Packaging, Release

### Dashboard (`apps/dashboard`)
- [ ] Next.js 16 + React 19 + Tailwind v4 + shadcn/ui scaffold
- [ ] Pages: Overview, Models, Providers, API Keys, Settings
- [ ] TanStack Table for lists, Recharts for overview
- [ ] Confirm no model traffic passes through Next.js (PRD §16)

### Packaging ✅
- [x] Multi-stage Dockerfile, distroless nonroot
- [x] `docker/compose.yaml` — router + named `router-data` volume for `/app/data`, config bind-mounted read-only
- [x] Multi-arch build: `linux/amd64` + `linux/arm64` — `Dockerfile` cross-compiles from `$BUILDPLATFORM` via `TARGETOS`/`TARGETARCH`; `make docker-multiarch` and `make release`
- [x] `packaging/systemd/zealish-router.service` — `DynamicUser`, `StateDirectory`, SIGTERM into the graceful shutdown path, hardened sandbox
- [x] `.goreleaser.yaml` — static linux amd64/arm64 tarballs with README, LICENSE, config and unit file; `make snapshot`
- [x] `version` command + `-X main.version` stamping, resolvable without a config file

### Hardening
- [x] Request body size limit — `server.max_body_bytes` (default 4 MiB) enforced by `limitBody` on `/v1` and `/api/v1`, oversized bodies get a 413 JSON envelope
- [x] Per-key rate limiting — **deferred to post-1.0** (Open Decision #4). A reverse proxy in front of the gateway covers the deployment case; an in-process limiter needs a quota model on `api_keys` that is not worth blocking the release
- [x] Panic recovery verified to not leak internals to clients — own `recoverer` replaces chi's, logs panic + stack, writes a generic 500 envelope, re-panics on `http.ErrAbortHandler`
- [x] Redact API keys from all log output — `provider.redact` strips the credential from upstream and transport error messages, which feed both logs and client responses
- [x] `-race` test run in CI

### Repo hygiene
- [x] `LICENSE` — Apache-2.0 (PRD header)
- [x] `README.md` — quickstart, config reference, coding-agent setup examples
- [x] `.gitignore` — `bin/`, `data/`, `.env`
- [x] `.github/workflows/ci.yml` — gofmt, vet, build, `test -race`, golangci-lint
- [x] `.github/workflows/release.yml` — on `v*` tags: goreleaser binaries + multi-arch image pushed to GHCR
- [x] `.golangci.yml` — errcheck, errorlint, revive, staticcheck, bodyclose, noctx; repo is lint-clean
- [x] `CONTRIBUTING.md` — local workflow, required checks, layout, conventions, how to add a provider

---

## Open Decisions

| # | Decision | Options | Status |
|---|----------|---------|--------|
| 1 | SQLite driver | `modernc.org/sqlite` (pure Go) vs `mattn/go-sqlite3` (cgo) | **resolved: `modernc.org/sqlite`** (v0.5) |
| 2 | Config as source of truth vs DB | YAML-only, DB-only, or YAML seeds DB | **resolved: DB-only** (v0.7) |
| 3 | Streaming fallback after first byte | commit vs inject error chunk | **resolved: commit** (v0.4) |
| 4 | Rate limiting scope | v1.0 vs post-1.0 | **resolved: post-1.0** (v1.0) |
| 5 | `Message.Content` representation | `json.RawMessage` vs typed union | **resolved: `json.RawMessage` + `Text()`** (v0.2) |

---

## Next Action

**v1.0 — dashboard is all that is left.** Backend, hardening, CI, packaging and repo hygiene are done: the repo is lint-clean, tested under `-race`, ships multi-arch images and static tarballs, a compose file and a hardened systemd unit. Remaining for the release: the Next.js dashboard in `apps/dashboard` (Overview, Models, Providers, API Keys, Settings) consuming `/api/v1`, then tagging. Per-key rate limiting is deferred to post-1.0.
