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
- Providers: OpenAI, OpenRouter, Ollama
- Model aliases with a deterministic fallback chain
- Bounded retry with exponential backoff, then fallback to the next provider
- Server-sent-events streaming with heartbeats and client-disconnect handling
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

Validate the shipped config, then start the server:

```sh
./bin/zealish-router -config config.yaml validate
./bin/zealish-router -config config.yaml serve
```

The database is created automatically at `database.path` (`data/router.db`).

### Add a provider and a model alias

Providers and model aliases live in the **database**, not in `config.yaml`.
Configure them through the admin API:

```sh
ADMIN=http://localhost:8787/api/v1
TOKEN=change-me   # admin.token from config.yaml

curl -X PUT $ADMIN/providers/openai \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"kind":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-…","timeout_ms":60000,"enabled":true}'

curl -X PUT $ADMIN/providers/ollama \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"kind":"ollama","base_url":"http://localhost:11434/v1","timeout_ms":300000,"enabled":true}'

curl -X PUT $ADMIN/models/gpt-4o \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"provider":"openai","model":"gpt-4o","fallback":["ollama"]}'
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

database:
  path: data/router.db

auth:
  enabled: true
  api_keys:                # static keys, in addition to database-backed keys
    - zr_local_dev

admin:
  enabled: true            # false unmounts /api/v1 entirely
  token: change-me
  cors_origins:
    - http://localhost:3000
```

| Key | Meaning |
|---|---|
| `server.write_timeout` | Keep at `0s`; a non-zero deadline truncates long streams. |
| `database.path` | SQLite file; parent directories are created on demand. |
| `auth.enabled` | `false` disables gateway authentication entirely. |
| `auth.api_keys` | Static keys compared in constant time; useful for local dev. |
| `admin.token` | Bearer token for `/api/v1`. Empty means the admin API fails closed. |
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
models                       print the alias → provider/model/fallback table
```

---

## HTTP API

### Gateway (`/v1`, gateway key)

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/chat/completions` | OpenAI chat completions, streaming and non-streaming |
| `GET` | `/v1/models` | Configured aliases |

### Operations (no auth)

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness |
| `GET` | `/metrics` | Prometheus exposition |

### Admin (`/api/v1`, admin token)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/overview` | Request counts, error rate, active streams, resource counts |
| `GET` `POST` `DELETE` | `/api/v1/keys[/{id}]` | Gateway keys; the raw key is returned only on create |
| `GET` `PUT` `DELETE` | `/api/v1/providers[/{name}]` | Providers; secrets are never serialised back, an omitted `api_key` keeps the stored one |
| `GET` `PUT` `DELETE` | `/api/v1/models[/{alias}]` | Model aliases and fallback chains |
| `GET` `PUT` | `/api/v1/settings` | Key/value settings |

Every mutation republishes the routing table in place — no restart needed.

---

## Routing and fallback

A model alias names a primary provider plus an ordered fallback chain. On a
retryable failure (timeout, rate limit, 5xx, connection error) the router
retries the same provider with exponential backoff and jitter, then advances to
the next provider in the chain. A 4xx response is returned immediately without
fallback. For streaming, fallback is only possible before the first chunk is
flushed; once bytes are on the wire the response is committed.

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

---

## Docker

```sh
make docker
docker run -p 8787:8787 \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  zealish-router:0.1.0
```

The image is multi-stage and runs as nonroot on distroless.

---

## Development

```sh
make build    # static binary into bin/
make run      # go run with config.yaml
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt -l -w .
```

Layout:

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
```

See `PRD.md` for the product spec and `TODO.md` for the milestone checklist.

---

## License

Apache-2.0. See [LICENSE](LICENSE).
