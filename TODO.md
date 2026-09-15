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

### Dashboard (`apps/dashboard`) ✅
- [x] Next.js 16 + React 19 + Tailwind v4 + shadcn/ui scaffold
- [x] Pages: Overview, Models, Providers, API Keys, Settings
- [x] TanStack Table for lists (pinned to v8 — v9 replaces `useReactTable` with a different `useTable` API), Recharts area chart on Overview
- [x] Admin token held in `localStorage` and sent as `Authorization: Bearer`, never baked into the build; `NEXT_PUBLIC_ROUTER_URL` points at the router
- [x] Confirm no model traffic passes through Next.js (PRD §16) — the client only calls `/api/v1`, `/v1` is never touched
- [x] `next build` and `eslint` clean; CRUD verified end to end against a running router

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
- [x] `.github/workflows/ci.yml` — gofmt, vet, build, `test -race`, golangci-lint, dashboard `eslint` + `next build`
- [x] `.github/workflows/release.yml` — on `v*` tags: goreleaser binaries + multi-arch image pushed to GHCR
- [x] `.golangci.yml` — errcheck, errorlint, revive, staticcheck, bodyclose, noctx; repo is lint-clean
- [x] `CONTRIBUTING.md` — local workflow, required checks, layout, conventions, how to add a provider

---

## v1.1 — Cost Accountability ✅

### Per-key attribution
- [x] Migration `0008_key_usage.sql` — `usage_events.key_id` + `(key_id, created_at)` index; pre-existing rows read as unattributed
- [x] `router.callMeta` carries the authenticated key id and start time through `dispatch`, replacing the bare `started` parameter
- [x] `storage.UsageStore.ByKey` / `KeySpend` — per-key aggregation and a single narrow spend read for the budget check
- [x] `GET /api/v1/usage/keys` — spend and tokens per key for a window; deleted keys and static traffic report as `unattributed`
- [x] `GET /api/v1/keys` joins in lifetime usage and current-month spend
- [x] Dashboard Keys page: requests, total cost, month-spend-against-budget and rate-limit columns

### Retention
- [x] `usage.retention_days` config (0 = keep everything, validated non-negative)
- [x] `storage.PruneUsage` — hourly sweep, immediate first pass so a shortened window applies on restart, bounded per-sweep timeout
- [x] `storage.UsageStore.Prune` on SQLite and memory

### Per-key quotas (resolves Open Decision #4)
- [x] Migration `0009_key_quotas.sql` — `api_keys.rate_limit_per_min`, `api_keys.monthly_budget_usd`, both 0 = unlimited
- [x] `auth.Quota` — rolling-minute window per key, monthly spend cached for 30s, read failure degrades to allow
- [x] `auth.Identity` carries the quotas, so the request path enforces them without a second storage read
- [x] `enforceQuota` middleware on `/v1` — 429 + `Retry-After`, `X-RateLimit-Limit`/`-Remaining`, `rate_limit_error` vs `insufficient_quota`
- [x] `PUT /api/v1/keys/{id}/quota` + dashboard dialog; editing or revoking a key drops its cached window and spend

### Tests
- [x] Storage: prune drops only rows before the cutoff and is idempotent; `ByKey`/`KeySpend` aggregate per key, group unattributed traffic and ignore prior months
- [x] Quota: unmetered identities bypass, window slides rather than resetting, keys are isolated, budget blocks only after the cache expires, `Forget` clears state, nil `Quota` allows everything
- [x] HTTP: 429 envelope and headers for both limits, no headers for an unlimited key, budget message leaks no key id
- [x] Admin: quota create/update/validation lifecycle, per-key usage joins, `usage/keys` ranking and `unattributed` naming
- [x] Migrations verified idempotent against the existing production database

---

## v1.2 — Embeddings ✅

- [x] `openai.EmbeddingRequest` / `EmbeddingResponse` — `input` and `embedding`
      stay `json.RawMessage`, so string/array input and float/base64 output all
      pass through untouched; both carry `Extra` like the chat types
- [x] `provider.Provider` gains `Embeddings`; `httpProvider` posts to
      `{base_url}/embeddings` and reuses the shared error mapping
- [x] `provider.ErrUnsupported` — terminal sentinel; the Anthropic dialect
      returns it instead of translating a request to a path that does not exist
- [x] `dispatch` generalised from `*ChatCompletionRequest` to a model name, so
      embeddings inherit alias resolution, fallback, retry, backoff and the
      circuit breaker unchanged; the per-call closure now rewrites its own
      upstream model
- [x] `Engine.Embeddings` — no streaming variant
- [x] `POST /v1/embeddings` behind the same auth, quota and body limit as
      `/v1/chat/completions`; `ErrUnsupported` maps to 400
- [x] Usage log and pricing: `embeddingUsage` prefers reported prompt tokens and
      estimates from the input otherwise; `text-embedding-*` rates added

### Tests
- [x] Provider: happy path hits `/embeddings` with the credential and forwards
      `input` verbatim; 429 maps to `ErrRateLimited`; Anthropic reports
      `ErrUnsupported` and it is not retryable
- [x] Router: retryable failure falls back to the next route, terminal 4xx does
      not
- [x] HTTP: happy path returns the vector and the resolved upstream model,
      missing `model`/`input` are 400, unknown alias 404, upstream 5xx is a 502
      with no detail leak, `ErrUnsupported` is a 400

---

## v1.3 — Tool Calling & Multimodal Across Dialects ✅

The OpenAI dialect passes `tools`, `tool_calls` and array-of-parts content
through untouched via `Extra`; only the Anthropic dialect had to learn them, so
the work lands in `internal/provider/anthropic_tools.go`.

### Request translation
- [x] `tools[].function` → Anthropic `tools[]` with `input_schema`; a tool
      without `parameters` gets an empty object schema, which Anthropic requires
      and OpenAI treats as optional
- [x] Non-function tool types (`web_search_preview`, …) are dropped rather than
      forwarded into a rejection
- [x] `tool_choice` string and object forms → `auto`/`none`/`any`/`tool`; only
      sent when a tools array survived translation
- [x] Assistant `tool_calls` → `tool_use` content blocks, the JSON-string
      `arguments` parsed back into the `input` object
- [x] `role: "tool"` + `tool_call_id` → user message with a `tool_result`
      block; a tool message without an id is dropped
- [x] Array-of-parts content → typed blocks; `image_url` with a `data:` URL
      becomes a `base64` source, a remote URL becomes a `url` source
- [x] String content stays a string, so plain conversations keep the compact
      wire form

### Response translation
- [x] `tool_use` blocks → `tool_calls` on the assistant message, `input`
      rendered back as the JSON string OpenAI clients parse
- [x] Streaming: `content_block_start` emits the opening delta with id and
      name, `input_json_delta` emits argument fragments, both indexed per
      content block so a client can concatenate them
- [x] `stop_reason: tool_use` already mapped to `finish_reason: "tool_calls"`

### Tests
- [x] Tools: function tools converted, non-function dropped, missing
      parameters defaulted, `tool_choice` across every form, choice suppressed
      without tools
- [x] Conversation: assistant call plus tool result round-trip, orphan tool
      message dropped
- [x] Multimodal: data URL and remote URL sources, string content preserved
- [x] Response: `tool_calls` present on the decoded *and* serialised message
- [x] Streaming: id, name and argument fragments reassemble into the original
      arguments object

---

## v1.4 — Per-Key Model Allowlist ✅

A key could previously address every alias and combo. The allowlist scopes a
credential to a subset of the model namespace, complementing the quotas from
v1.1: quotas cap how much a key spends, the allowlist caps what it can reach.

### Storage
- [x] Migration `0014_key_model_allowlist.sql` — `api_keys.allowed_models`,
      comma-separated like fallback chains; empty means unrestricted, so keys
      created before the migration keep their current reach
- [x] `storage.APIKey.AllowedModels` + `APIKeyStore.SetAllowedModels` on SQLite
      and memory; `Create` now persists quotas and the allowlist, which the
      previous insert silently dropped

### Request path
- [x] `auth.Identity.AllowedModels` carried off the key record, so enforcement
      costs no second storage read
- [x] `Identity.Allows` — exact match; aliases and combos share one namespace,
      so no prefix or pattern logic
- [x] `allowModel` in the handler, not a middleware: the model name lives in
      the body, which only the handler has decoded. 403, not 404 — the model
      exists, this key just cannot reach it
- [x] Enforced on `/v1/chat/completions` (streaming included, before any SSE
      frame is committed) and `/v1/embeddings`
- [x] `GET /v1/models` lists only what the key may call, so discovery matches
      what dispatch permits

### Admin & dashboard
- [x] `allowed_models` on create and in every key response
- [x] `PUT /api/v1/keys/{id}/models` — validates every name against the engine's
      aliases and combos, so a typo fails at configuration time instead of
      silently locking the key out; duplicates collapse
- [x] Dashboard Keys page: Models column and an allowed-models dialog picking
      from aliases and combos

### Tests
- [x] Storage: allowlist round-trip, replace, clear, `ErrNotFound`, keys created
      without one read as unrestricted
- [x] Auth: `Allows` across exact/miss/empty/case, identity carries the list,
      static keys stay unrestricted
- [x] HTTP: allowed model passes, disallowed is a 403 on chat, stream and
      embeddings, the stream rejection is a status code and not an SSE frame,
      an empty allowlist reaches everything, `/v1/models` hides the rest
- [x] Admin: create/update/clear lifecycle, unknown model rejected on both
      paths, combos accepted, unknown key id is a 404
- [x] Migration verified idempotent against the existing production database

---

## v1.5 — Response Cache ✅

### Cache core (`internal/cache`)
- [x] LRU keyed on `sha256(endpoint ‖ model ‖ raw body)`, bounded by both a TTL
      and a max entry count; fields delimited so boundaries are unambiguous
- [x] A disabled cache is a nil `*Cache` whose every method is a no-op, so no
      caller branches on enablement
- [x] Counters for hits, misses, stores and evictions; `Purge` drops entries
      and preserves the counters

### Config
- [x] `cache.enabled` / `cache.ttl` / `cache.max_entries`, off by default with
      the TTL and size pre-filled so enabling takes one line
- [x] Validation rejects an enabled cache with a non-positive TTL or size —
      unbounded staleness or footprint is a misconfiguration, not a default

### Request path
- [x] Chat and embeddings buffer the raw body, key on it, and serve a hit
      without touching an upstream
- [x] Streaming bypasses the cache entirely: chunks are relayed, never buffered
- [x] Failures are never admitted, so an upstream error is not replayed
- [x] `X-Cache: HIT | MISS | BYPASS` on every response
- [x] Handlers encode once and reuse the bytes for both the client and the
      cache (`writeBody`)

### Observability & admin
- [x] `router_cache_events_total{endpoint,model,event}`
- [x] `GET /api/v1/cache` — occupancy, capacity, counters, hit rate, TTL
- [x] `DELETE /api/v1/cache` — purge, for an upstream that changed inside the
      TTL window

### Tests
- [x] Cache: miss then hit, TTL expiry, LRU eviction order, replace refreshing
      the TTL, purge preserving counters, nil-cache safety, key separation by
      endpoint/model/body, concurrent access under `-race`
- [x] HTTP: hit skips upstream and returns identical bytes, distinct bodies and
      models miss, streaming bypasses, embeddings hit, endpoints never collide,
      a failed request is not cached, a disabled cache always bypasses
- [x] Admin: stats reflect hits and misses, purge empties the cache and forces
      a refetch
- [x] Config: defaults, YAML round-trip, unbounded settings rejected

---

## v1.6 — Cache Panel ✅

The cache admin endpoints shipped in v1.5, but the hit rate was only visible
over HTTP or Prometheus. This puts it in the dashboard.

- [x] `apps/dashboard/src/app/cache/page.tsx` — hit rate, occupancy against
      capacity, TTL, and the hit/miss/store/eviction counters, polled every 5s
- [x] Purge button, disabled while the cache is empty, reporting how many
      entries were dropped
- [x] Disabled cache renders the `config.yaml` keys that enable it instead of
      a wall of zeroes
- [x] `CacheStats` type in `lib/api.ts` mirroring `cacheStatsResponse`;
      `api.del` is generic so the purge count can be read
- [x] Nav entry between Keys and Settings
- [x] `next build` and `eslint` clean

---

## v1.7 — Anthropic Ingress ✅

The Anthropic dialect was previously outbound only: the gateway could *call* an
Anthropic upstream, but a client had to speak OpenAI. This opens the other
edge, so Claude Code and the Anthropic SDKs reach the same aliases, fallback
chain, quotas and usage log — and can be routed onto a non-Anthropic upstream.

### Shared wire types
- [x] `pkg/anthropic` — the Messages types lifted out of `internal/provider`,
      which needed them for the outbound direction and now shares them with the
      inbound one; `internal/provider/anthropic*.go` repointed onto it
- [x] `Request.System` widened from `string` to `json.RawMessage` with
      `SystemText`: the API accepts a plain string *and* an array of text
      blocks, and a real client sends both
- [x] `ContentBlock` grew the `tool_result` and `image` fields the outbound
      direction had encoded as ad-hoc maps

### Request translation (Anthropic → OpenAI)
- [x] Top-level `system` becomes a leading system message
- [x] `tool_use` blocks → assistant `tool_calls`, the `input` object rendered
      back as the JSON-string `arguments` OpenAI clients parse
- [x] `tool_result` blocks → their own `role: "tool"` messages carrying
      `tool_call_id`; a turn holding several results expands into several
      messages, which is how that dialect models them
- [x] `image` blocks → `image_url` parts; a `base64` source becomes a data URL,
      a `url` source passes through
- [x] `tools[]`/`tool_choice` → the OpenAI nesting; Anthropic's `any` is
      OpenAI's `required`
- [x] A text-only turn keeps the compact string form, so a plain conversation
      does not grow an array of parts

### Response translation (OpenAI → Anthropic)
- [x] Assistant text and `tool_calls` → a `message` envelope with `text` and
      `tool_use` content blocks
- [x] `finish_reason` → `stop_reason`, the inverse of the existing mapping
- [x] Usage: cached and cache-written prompt tokens are reported on their own
      Anthropic fields and subtracted from `input_tokens`, so a cached prompt
      is not billed twice

### Streaming
- [x] `stream.Writer.NamedEvent` — the Anthropic dialect names every frame,
      which the OpenAI one never needed
- [x] `messageStream` reframes uniform OpenAI chunks into the bracketed event
      sequence: `message_start` → (`content_block_start` →
      `content_block_delta*` → `content_block_stop`)* → `message_delta` →
      `message_stop`, with no `[DONE]` sentinel
- [x] Text and tool calls never share a content block; a switch between them
      closes the open block and advances the index
- [x] Tool arguments stream as `input_json_delta` fragments, indexed per block
      so a client can concatenate them
- [x] Keepalives are `ping` events rather than comment frames
- [x] An upstream that closes without a chunk still produces a well-formed
      message rather than an empty body

### Transport
- [x] `POST /v1/messages` behind the same body limit, auth, quota, allowlist
      and trace middleware as `/v1/chat/completions`
- [x] `max_tokens` required, unlike the OpenAI dialect which treats it as
      optional
- [x] Errors answer in the Anthropic envelope — `writeGatewayError` picks the
      dialect by endpoint, so auth and quota middleware shared with `/v1` never
      hand an Anthropic client an OpenAI error body
- [x] Cached under its own endpoint label: the bodies are a different dialect,
      so an identical prompt is not an identical request

### Tests
- [x] Handler: happy path, required fields, unknown alias, upstream 5xx as a
      502 with no detail leak, all in the Anthropic envelope
- [x] Translation: full tool cycle round-trips, images become data URLs, usage
      is not double-counted
- [x] Streaming: event sequence and ordering, no `[DONE]`, text deltas
      reassemble, tool fragments reassemble into the original arguments, text
      and tool calls occupy separate indexed blocks, usage on `message_delta`,
      pre-frame failure is a status code, cache bypassed
- [x] `stream`: named and unnamed frames; `pkg/anthropic`: `SystemText` across
      every accepted shape

---

## v1.8 — Dialect Visibility ✅

v1.7 shipped the Anthropic endpoint but left the dashboard unable to tell the
two client populations apart: both dialects resolve the same aliases, so a
trace looked identical whichever one produced it.

### Storage
- [x] Migration `0015_trace_dialect.sql` — `request_traces.dialect`, defaulting
      to `openai`: every row predating the Anthropic endpoint was OpenAI
      traffic, so the backfill is the default rather than a nullable column
- [x] `storage.RequestTrace.Dialect` + `TraceFilter.Dialect` on SQLite and
      memory; `dialectOrDefault` keeps the column non-empty whichever store
      wrote it
- [x] `storage.DialectOpenAI` / `DialectAnthropic` — the dialect is recorded,
      never inferred from a request path

### Request path
- [x] `router.WithDialect` / `DialectFrom` carry it through the request
      context, defaulting to OpenAI so only the Anthropic handler sets it
- [x] The trace records it; routing never reads it, since both dialects
      resolve the same aliases

### Admin & dashboard
- [x] `dialect` on every trace response and as a `GET /api/v1/requests` filter
- [x] `REQUEST_DIALECTS` in `lib/api.ts` and a `DialectBadge` component
- [x] Request Trace page: Dialect column and an all-dialects filter alongside
      status, model and provider
- [x] Request detail page: Client dialect field
- [x] Metrics needed no new label — `router_requests_total` already separates
      by the `path` label and the response cache by its `endpoint` label

### Tests
- [x] Storage: dialect round-trips, a trace recorded without one reads as
      OpenAI, filtering isolates each population
- [x] HTTP: a `/v1/messages` call and a `/v1/chat/completions` call produce one
      trace each with the right dialect, and the admin filter returns only one
- [x] Migration verified idempotent against the existing production database
- [x] `next build`, `tsc` and `eslint` clean; verified in a browser against a
      running router

## v1.9 — Extension Effect Tracking ✅

v1.8's Anthropic ingress work landed alongside RTK and Request Sanitization
(the two toggleable request extensions), but that commit shipped without
tests and without a way to see what either extension actually saves. This
closes both gaps.

### Effect tracking
- [x] `extension.Stats` — lifetime messages rewritten, bytes saved, and bytes
      expressed as tokens at the same ratio the router's usage estimator uses
- [x] `Registry.Apply` diffs message-list byte totals before/after sanitize
      and reads RTK's own `Result.SavedBytes`/`CompressedMessages`, both
      behind atomic counters so the hot path never blocks on the config lock
- [x] `GET /api/v1/extensions` and the dashboard Extensions page report each
      extension's stats alongside its enabled flag

### Tests
- [x] `internal/extension`: defaults-disabled, no-op when nothing enabled,
      update persists and reloads, unknown id and negative `history_window`
      rejected, sanitize and RTK stats populate after a rewrite, system
      prompts stay untouched with both enabled
- [x] `internal/extension/sanitize`: whitespace trim, system/developer
      exemption, no-op returns the input unchanged, history window keeps the
      tail and never opens on an orphaned tool result, dedup drops
      consecutive duplicates but never a system prompt or a tool call
- [x] `internal/api`: `/api/v1/extensions` list and update lifecycle,
      unknown id 404, invalid config 400, disabled admin API 404, a chat
      request is rewritten when an extension is enabled and passed through
      untouched when it is not
- [x] `next build`, `tsc` and `eslint` clean

---

## Open Decisions

| # | Decision | Options | Status |
|---|----------|---------|--------|
| 1 | SQLite driver | `modernc.org/sqlite` (pure Go) vs `mattn/go-sqlite3` (cgo) | **resolved: `modernc.org/sqlite`** (v0.5) |
| 2 | Config as source of truth vs DB | YAML-only, DB-only, or YAML seeds DB | **resolved: DB-only** (v0.7) |
| 3 | Streaming fallback after first byte | commit vs inject error chunk | **resolved: commit** (v0.4) |
| 4 | Rate limiting scope | v1.0 vs post-1.0 | **resolved: shipped in v1.1**, in-process, keyed on `api_keys` |
| 5 | `Message.Content` representation | `json.RawMessage` vs typed union | **resolved: `json.RawMessage` + `Text()`** (v0.2) |
| 6 | Anthropic ingress representation | reuse the OpenAI core vs a parallel pipeline | **resolved: reuse** (v1.7) — `/v1/messages` translates at the edge, so routing, quotas and usage stay single-sourced |

---

## Next Action

**v1.9 is feature-complete.** Both edges speak either dialect, and the
dashboard reports which one each client used. RTK and Request Sanitization
are tested end to end, and their effect on request bodies is visible in the
dashboard rather than assumed. No further work is queued.
