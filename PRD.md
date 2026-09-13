# PRD.md

# Zealish Router

Lightweight OpenAI-Compatible AI Gateway written in Go

Version: 1.0 (MVP)

Status: Draft

License: Apache-2.0

Platform: Linux (Fedora First), Docker, VPS

---

# 1. Overview

Zealish Router adalah AI Gateway open-source yang menyediakan endpoint OpenAI-compatible untuk berbagai LLM provider.

Fokus utama proyek adalah menjadi single binary yang ringan, cepat, mudah di-deploy, dan dapat digunakan oleh coding agents seperti Codex CLI, OpenCode, Cline, Cursor, Roo Code, maupun aplikasi yang sudah mendukung OpenAI SDK.

MVP hanya mengimplementasikan OpenAI Chat Completions API.

---

# 2. Goals

- OpenAI-compatible REST API
- Single binary Go
- Multi-provider melalui satu endpoint
- Auto fallback antar provider
- Streaming SSE
- YAML configuration
- Dashboard terpisah (Next.js)
- Linux native deployment

---

# 3. Non Goals (MVP)

- Claude native API
- OpenAI Responses API
- Image generation
- Audio / Whisper
- Function Calling
- MCP Server
- RBAC multi user
- Billing
- Team management

---

# 4. Target Users

- AI developers
- Open source contributors
- Linux users
- Self-hosters
- Coding agent users

---

# 5. Architecture

                   +----------------------+
                   |  Codex / Cline /    |
                   |  OpenCode / Cursor  |
                   +----------+-----------+
                              |
                              |
                OpenAI Compatible HTTP API
                              |
                              v
              +------------------------------+
              |       Zealish Router         |
              |------------------------------|
              | Authentication               |
              | Model Alias                 |
              | Router Engine               |
              | Retry & Fallback            |
              | Streaming SSE              |
              | Metrics                    |
              +-------------+--------------+
                            |
        +-------------------+-------------------+
        |                   |                   |
        v                   v                   v
   OpenAI API        OpenRouter API      Ollama API

Dashboard tidak berada di jalur request model.

Dashboard hanya mengakses REST API internal.

---

# 6. Technology Stack

## Core

- Go 1.25+
- Chi v5
- slog
- SQLite
- Prometheus
- fsnotify
- YAML

## Dashboard

- Next.js 16
- React 19
- Tailwind CSS v4
- shadcn/ui
- TanStack Table
- Recharts

---

# 7. Repository Structure

```text
zealish-router/
│
├── apps/
│   ├── server/
│   └── dashboard/
│
├── internal/
│   ├── api/
│   ├── auth/
│   ├── config/
│   ├── provider/
│   ├── router/
│   ├── stream/
│   ├── metrics/
│   └── storage/
│
├── pkg/
│   └── openai/
│
├── configs/
│   └── config.yaml
│
├── docker/
│
├── Makefile
│
└── go.mod
```

---

# 8. API Specification

## Health

GET /health

Response

```json
{
  "status": "ok"
}
```

---

## Models

GET /v1/models

Response

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-5",
      "object": "model"
    }
  ]
}
```

---

## Chat Completions

POST /v1/chat/completions

Request

```json
{
  "model": "gpt-5",
  "stream": true,
  "messages": [
    {
      "role": "user",
      "content": "Hello"
    }
  ]
}
```

Response mengikuti spesifikasi OpenAI.

Streaming menggunakan Server Sent Events.

---

# 9. Provider Interface

Semua provider wajib mengimplementasikan interface berikut.

```go
type Provider interface {
    Name() string

    ChatCompletion(
        ctx context.Context,
        req *ChatCompletionRequest,
    ) (*ChatCompletionResponse, error)

    ChatCompletionStream(
        ctx context.Context,
        req *ChatCompletionRequest,
    ) (<-chan StreamChunk, error)
}
```

Implementasi provider:

- OpenAI
- OpenRouter
- Ollama

Provider lain ditambahkan tanpa mengubah Router Engine.

---

# 10. Model Alias

Client tidak mengetahui provider.

Contoh

```yaml
models:

  gpt-5:
    provider: openai
    model: gpt-5

  fast:
    provider: openrouter
    model: openai/gpt-5-mini

  local:
    provider: ollama
    model: qwen3:32b
```

Client selalu mengirim

```json
{
  "model": "gpt-5"
}
```

Router menentukan provider sebenarnya.

---

# 11. Fallback

Jika provider utama gagal karena:

- timeout
- rate limit
- 5xx
- connection error

Router otomatis mencoba provider berikutnya.

Contoh

```yaml
fallback:

  gpt-5:
    - fast
    - local
```

Urutan bersifat deterministic.

---

# 12. Authentication

Header

```text
Authorization: Bearer zr_xxxxxxxxx
```

API key disimpan di SQLite.

Field

| Field | Description |
|--------|-------------|
| id | UUID |
| name | Display name |
| key_hash | SHA256 hash |
| created_at | Timestamp |
| last_used_at | Timestamp |
| enabled | Boolean |

Raw API key tidak pernah disimpan.

---

# 13. Configuration

config.yaml

```yaml
server:

  host: 0.0.0.0
  port: 8787

database:

  path: data/router.db

auth:

  api_keys:
    - zr_local_dev

providers:

  openai:
    base_url: https://api.openai.com/v1
    api_key: sk-xxxxx

  openrouter:
    base_url: https://openrouter.ai/api/v1
    api_key: or-xxxxx

  ollama:
    base_url: http://127.0.0.1:11434/v1

models:

  gpt-5:
    provider: openai
    model: gpt-5

  fast:
    provider: openrouter
    model: openai/gpt-5-mini

fallback:

  gpt-5:
    - fast
```

Perubahan file otomatis direload tanpa restart.

---

# 14. Database Schema

SQLite

## api_keys

| Column | Type |
|----------|------|
| id | TEXT |
| name | TEXT |
| key_hash | TEXT |
| enabled | INTEGER |
| created_at | INTEGER |
| last_used_at | INTEGER |

## providers

| Column | Type |
|----------|------|
| id | TEXT |
| name | TEXT |
| base_url | TEXT |
| api_key | TEXT |
| enabled | INTEGER |

## model_aliases

| Column | Type |
|----------|------|
| alias | TEXT |
| provider | TEXT |
| model | TEXT |

---

# 15. Metrics

GET /metrics

Prometheus compatible.

Contoh metric

```text
router_requests_total

router_request_duration_seconds

router_provider_errors_total

router_stream_connections

router_tokens_total
```

---

# 16. Dashboard

Dashboard berjalan terpisah.

Default

| Service | Port |
|-----------|------|
| Router | 8787 |
| Dashboard | 3000 |

Dashboard hanya menggunakan REST API.

Halaman MVP

- Overview
- Models
- Providers
- API Keys
- Settings

Tidak ada request model yang melewati Next.js.

---

# 17. CLI

Start server

```bash
zealish-router serve
```

Validate config

```bash
zealish-router validate
```

Generate API key

```bash
zealish-router keys create
```

List models

```bash
zealish-router models
```

---

# 18. Deployment

Binary

```bash
./zealish-router serve
```

Docker

```bash
docker compose up -d
```

Systemd

```bash
systemctl enable zealish-router
```

Target platform

- Fedora
- Ubuntu
- Debian
- Docker
- ARM64
- AMD64

---

# 19. Milestones

## v0.1

- HTTP server
- Health endpoint
- Config loader

## v0.2

- Chat Completions
- Streaming SSE

## v0.3

- OpenAI provider
- OpenRouter provider
- Ollama provider

## v0.4

- Model alias
- Auto fallback
- Retry

## v0.5

- SQLite
- API keys
- Metrics

## v1.0

- Dashboard
- Docker
- Stable release

---

# 20. Design Principles

- OpenAI Compatible First
- Single Binary Core
- Dashboard Is Optional
- Provider Agnostic
- Zero Vendor Lock-in
- Clean Architecture
- Minimal Dependencies
- Production Ready

---

# Tagline

Zealish Router — A lightweight, single-binary OpenAI-compatible AI gateway written in Go.
