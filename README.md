# Zealish Router

Lightweight OpenAI-compatible AI gateway written in Go. One endpoint, many
providers, automatic fallback, SSE streaming — shipped as a single static
binary.

Point any OpenAI SDK or coding agent (Codex CLI, OpenCode, Cline, Cursor, Roo
Code) at `http://localhost:8787/v1` and route it to OpenAI, OpenRouter, or a
local Ollama instance.

License: Apache-2.0 · Platform: Linux, Docker

---

## Features

- OpenAI-compatible `POST /v1/chat/completions` and `GET /v1/models`
- Two wire dialects — OpenAI and Anthropic — plus a preset catalogue for known
  upstreams (OpenAI, OpenRouter, Groq, Ollama, …); any compatible endpoint works
- Model aliases with a deterministic fallback chain
- Combos: one virtual model name backed by a pool of aliases, `fallback` or
  `round_robin`
- Bounded retry with exponential backoff, then fallback to the next provider
- Server-sent-events streaming with heartbeats and client-disconnect handling
- Durable usage log: every request — successful or failed — is recorded with
  tokens, cost and status for lifetime statistics
- Built-in pricing table for cost attribution per request
- Outbound proxy pool for reaching upstreams through rotating proxies
- API key authentication (`zr_…` keys, only hashes stored)
- SQLite persistence, pure Go — no cgo, `CGO_ENABLED=0` friendly
- Prometheus metrics at `/metrics`
- Admin REST API at `/api/v1` for the dashboard and tooling
- Live reload of `config.yaml` without a restart

---

## Quickstart

```sh
git clone https://github.com/zealish/zealish-router
cd zealish-router
make build
```

Copy the template, validate it, then start the server. `config.yaml` is
gitignored so your admin token never lands in version control:

```sh
cp config.example.yaml config.yaml
./bin/zealish-router -config config.yaml validate
./bin/zealish-router -config config.yaml serve
```

The database is created automatically at `database.path` (`data/router.db`).

### Add a provider and a model alias

Providers and model aliases live in the **database**, not in `config.yaml`.
Configure them through the admin API:

```sh
ADMIN=http://localhost:8787/api/v1
TOKEN=$(openssl rand -hex 32)
```

The admin API ships **disabled** so a default deployment exposes no
configuration surface. Enable it in `config.yaml` with the token you just
generated, then restart:

```yaml
admin:
  enabled: true
  token: <your token>
```

Then configure providers and aliases:

```sh
curl -X PUT $ADMIN/providers/openai \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"kind":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-…","timeout_ms":60000,"enabled":true}'

curl -X PUT $ADMIN/providers/ollama \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"kind":"ollama","base_url":"http://localhost:11434/v1","timeout_ms":300000,"enabled":true}'

curl -X PUT $ADMIN/models/local \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"provider":"ollama","model":"llama3.1"}'

# fallback lists other *aliases*, tried in order after the primary fails
curl -X PUT $ADMIN/models/gpt-4o \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"provider":"openai","model":"gpt-4o","fallback":["local"]}'
```

Check the routing table:

```sh
./bin/zealish-router models
```

### Issue a gateway key

```sh
./bin/zealish-router keys create --name laptop
# id:  …
# key: zr_…    <- shown once, only the hash is stored
```

### Call the gateway

```sh
curl http://localhost:8787/v1/chat/completions \
  -H "Authorization: Bearer zr_…" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
```

Streaming is the same request with `"stream": true`; the response is an SSE
stream terminated by `data: [DONE]`.

Using an OpenAI SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8787/v1", api_key="zr_…")
client.chat.completions.create(model="gpt-4o", messages=[{"role": "user", "content": "hi"}])
```

---

## Configuration

`config.yaml` holds process-level concerns only. It is watched and reloaded
live; an invalid file is rejected and the previous configuration stays active.

```yaml
server:
  host: 0.0.0.0
  port: 8787
  read_timeout: 30s
  write_timeout: 0s        # 0 = no write deadline, required for streaming
  shutdown_timeout: 15s
  max_body_bytes: 4194304  # 4 MiB cap on request bodies; 0 disables it

database:
  path: data/router.db

auth:
  enabled: true
  api_keys: []             # static keys, in addition to database-backed keys

usage:
  retention_days: 90       # delete usage events older than this; 0 keeps all

admin:
  enabled: false           # false unmounts /api/v1 entirely
  token: ""                # generate your own; validation rejects an empty
                           # token while admin.enabled is true
  cors_origins:
    - http://localhost:3000
```

| Key | Meaning |
|---|---|
| `server.write_timeout` | Keep at `0s`; a non-zero deadline truncates long streams. |
| `server.max_body_bytes` | Request bodies above this size are rejected with 413. `0` disables the cap. |
| `database.path` | SQLite file; parent directories are created on demand. |
| `auth.enabled` | `false` disables gateway authentication entirely. |
| `auth.api_keys` | Static keys compared in constant time; useful for local dev. |
| `usage.retention_days` | Usage events older than this are deleted hourly. `0` keeps every event, growing the database without bound. |
| `admin.enabled` | Ships `false`, so a default deployment exposes no configuration surface. |
| `admin.token` | Bearer token for `/api/v1`. Required once `admin.enabled` is true. |
| `admin.cors_origins` | Exact origins echoed back; wildcards are never sent. |

---

## CLI

```
zealish-router [-config config.yaml] [-log-level info] <command>

serve                        run the gateway (default)
validate                     load and validate the configuration
keys create --name <name>    create a gateway key, print it once
keys list                    list keys with creation and last-used times
keys revoke <id>             delete a key
models                       print the alias and combo routing tables
version                      print the build version
```

---

## HTTP API

### Gateway (`/v1`, gateway key)

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/chat/completions` | OpenAI chat completions, streaming and non-streaming |
| `GET` | `/v1/models` | Configured aliases and combos |

### Operations (no auth)

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness |
| `GET` | `/metrics` | Prometheus exposition |

### Admin (`/api/v1`, admin token)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/overview` | Usage totals for a window (`?hours=`, default lifetime) plus live counters and resource counts |
| `GET` | `/api/v1/usage` | Time-bucketed token/cost series for a window (`?hours=`) |
| `GET` | `/api/v1/usage/recent` | Newest usage events (`?limit=`) |
| `GET` | `/api/v1/usage/keys` | Spend and tokens per API key for a window (`?hours=`) |
| `GET` `POST` `DELETE` | `/api/v1/keys[/{id}]` | Gateway keys; the raw key is returned only on create |
| `PUT` | `/api/v1/keys/{id}/quota` | Set a key's `rate_limit_per_min` and `monthly_budget_usd` |
| `GET` | `/api/v1/provider-catalog` | Presets for known upstreams |
| `GET` `PUT` `DELETE` | `/api/v1/providers[/{name}]` | Providers; secrets are never serialised back, an omitted `api_key` keeps the stored one |
| `GET` `POST` | `/api/v1/providers/{name}/catalog`, `…/import` | List a provider's upstream models and import them as aliases |
| `GET` `PUT` `DELETE` | `/api/v1/models[/{alias}]` | Model aliases and fallback chains |
| `POST` | `/api/v1/models/{alias}/test` | Fire a minimal completion through an alias |
| `GET` `PUT` `DELETE` | `/api/v1/combos[/{name}]` | Combos: virtual models backed by a pool of aliases |
| `GET` `PUT` `POST` `DELETE` | `/api/v1/proxies[/{name}]`, `…/import` | Outbound proxy pool |
| `GET` `PUT` | `/api/v1/settings` | Key/value settings |

Every mutation republishes the routing table in place — no restart needed.

---

## Dashboard

A separate Next.js app in `apps/dashboard` — Overview, Models, Providers,
Combos, Proxies, API Keys and Settings — talking only to `/api/v1`. No model
traffic passes through it. The Overview page charts lifetime totals, cost and
token series from the usage log, the most recently used models, and the latest
requests.

```sh
cd apps/dashboard
cp .env.example .env.local     # NEXT_PUBLIC_ROUTER_URL, defaults to :8787
npm install
npm run dev                    # http://localhost:3000
```

Or run the router and the dashboard together from the repository root with
`make dev`; Ctrl-C stops both.

The router must have `admin.enabled: true` and list the dashboard origin under
`admin.cors_origins`. The admin token is entered in the browser and kept in
`localStorage`; it is never baked into the build.

---

## Routing and fallback

A model alias names a provider and an upstream model, plus an ordered chain of
other **aliases** to fall back to. On a retryable failure (timeout, rate limit,
5xx, connection error) the router retries the same provider with exponential
backoff and jitter, then advances to the next alias in the chain — which may
point at an entirely different provider and model. A 4xx response is returned
immediately without fallback. For streaming, fallback is only possible before
the first chunk is flushed; once bytes are on the wire the response is
committed.

### Combos

A combo is a virtual model: one client-facing name backed by an ordered pool of
aliases. Requesting the combo expands it into a chain — each member followed by
that member's own fallbacks — so a single name can span several providers and
accounts.

| Strategy | Behaviour |
|---|---|
| `fallback` | Always start at the first member and cascade down the pool. |
| `round_robin` | Rotate the starting member per request to spread quota, then cascade. |

```sh
curl -X PUT http://localhost:8787/api/v1/combos/code-agent \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"strategy":"round_robin","members":["gpt-5","fast","local"],"enabled":true}'
```

Combos are addressable wherever a model is: `"model": "code-agent"` on
`/v1/chat/completions`, and they appear in `GET /v1/models`. A combo never
shadows a real alias of the same name, members must be existing aliases, and
deleting a provider strips its aliases from every pool — a combo left without
members is dropped rather than routing into thin air.

---

## Metrics

| Metric | Labels |
|---|---|
| `router_requests_total` | `method`, `path`, `status` |
| `router_request_duration_seconds` | `method`, `path` |
| `router_provider_errors_total` | `provider`, `reason` |
| `router_stream_connections` | — |
| `router_tokens_total` | `provider`, `model`, `kind` |

Token counts come from upstream `usage` when reported and are estimated
(~4 characters per token) otherwise, including for streams.

### Usage log

Prometheus counters reset with the process, so the dashboard reads from a
durable per-request log in SQLite instead. Every request that reaches a
provider is recorded — successful ones with tokens and cost, failed ones with
their failure class (`timeout`, `rate_limited`, `upstream_5xx`, `connection`,
`canceled`, `client_error`) — so lifetime totals and error rates survive
restarts and count every hit on a model. One request is one row, regardless of
retries and fallbacks. Costs come from the built-in pricing table in
`internal/pricing`.

Each row is attributed to the API key that authenticated it, which is what
backs per-key cost reporting and monthly budgets. Rows written before
attribution existed, and traffic from static keys or an unauthenticated
gateway, report as `unattributed`. Set `usage.retention_days` to bound the
log; an hourly sweep deletes anything older.

### Per-key quotas

Each gateway key carries two optional limits, both zero (unlimited) by default
and editable from the dashboard or `PUT /api/v1/keys/{id}/quota`:

| Field | Meaning |
|---|---|
| `rate_limit_per_min` | Requests allowed in a rolling minute. Exceeding it returns `429` with `Retry-After: 60`. |
| `monthly_budget_usd` | Spend allowed in the current calendar month, priced from the usage log. Exceeding it returns `429` with type `insufficient_quota`. |

Limited keys get `X-RateLimit-Limit` and `X-RateLimit-Remaining` on every
response. Budget totals are cached for 30 seconds, so a key may overshoot
slightly under burst traffic — the budget is a cost guardrail, not a ledger.
Static keys and requests made with `auth.enabled: false` are unmetered.

---

## Deployment

### Docker Compose

```sh
docker compose -f docker/compose.yaml up -d
```

`config.yaml` is bind-mounted read-only and the database lives in the named
`router-data` volume.

### Docker

```sh
make docker
docker run -p 8787:8787 \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v router-data:/app/data \
  zealish-router:0.1.0
```

The image is multi-stage and runs as nonroot on distroless. Multi-arch images
(`linux/amd64`, `linux/arm64`) are cross-compiled from the build platform:

```sh
make docker-multiarch          # needs a buildx builder
```

### systemd

```sh
sudo install -m 0755 bin/zealish-router /usr/local/bin/zealish-router
sudo install -Dm 0644 config.yaml /etc/zealish-router/config.yaml
sudo install -Dm 0644 packaging/systemd/zealish-router.service \
  /etc/systemd/system/zealish-router.service

sudo systemctl daemon-reload
sudo systemctl enable --now zealish-router
```

The unit runs under `DynamicUser` with a hardened sandbox and gets
`/var/lib/zealish-router` from `StateDirectory` — set `database.path` to
`/var/lib/zealish-router/router.db`.

### Binaries

```sh
make release                   # static linux amd64 + arm64 into bin/
make snapshot                  # goreleaser tarballs without tagging
```

---

## Development

```sh
make build    # static binary into bin/
make run      # go run with config.yaml
make dev      # router + dashboard together
make test     # go test ./...
make race     # go test -race ./...
make lint     # golangci-lint run ./...
make vet      # go vet ./...
make fmt      # gofmt -l -w .
```

Layout:

```
cmd/server        entrypoint, CLI, dependency wiring
internal/api      HTTP transport: gateway, admin, middleware
internal/router   alias resolution, fallback chain, retry
internal/provider provider implementations, catalog presets, upstream error mapping
internal/stream   SSE writer and relay
internal/storage  SQLite and in-memory stores, migrations
internal/auth     key hashing, generation, authentication
internal/pricing  static price table for cost attribution
internal/config   YAML loading, validation, live reload
internal/metrics  private Prometheus registry
pkg/openai        OpenAI wire types
apps/dashboard    Next.js dashboard (Overview, Models, Providers, Combos, Proxies, Keys, Settings)
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution workflow, `PRD.md`
for the product spec and `TODO.md` for the milestone checklist.

---

## License

Apache-2.0. See [LICENSE](LICENSE).
