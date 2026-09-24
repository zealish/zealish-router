# PRD — Ponytail Extension for Zealish Router

**Product:** Zealish Router
**Version:** 1.0
**Status:** Draft
**Author:** Zealish
**Target:** OpenAI-Compatible AI Gateway

---

# 1. Overview

Ponytail is a request preprocessing extension that optimizes context before it reaches the target LLM. It is **not a model** and does not modify model behavior. Its responsibility is to reduce unnecessary tokens while preserving the highest possible semantic fidelity.

The extension sits inside the Zealish Router request pipeline and works with every OpenAI-compatible model.

Pipeline:

Client → Zealish Router → Ponytail → Provider → LLM

---

# 2. Goals

- Reduce input token usage automatically.
- Improve first-token latency by shrinking oversized context.
- Preserve important information with minimal semantic loss.
- Work transparently for every chat completion request.
- Support streaming without changing OpenAI API compatibility.

---

# 3. Non Goals

- No modification of model responses.
- No agent orchestration.
- No tool execution.
- No RAG database.
- No embeddings service.
- No prompt generation.
- No provider-specific behavior.

---

# 4. User Stories

### As a developer

I enable Ponytail once and every request is optimized automatically.

### As a coding user

Large repositories should consume fewer tokens without manually removing files.

### As an API user

The OpenAI API remains unchanged. Existing SDKs continue working.

---

# 5. Functional Requirements

## 5.1 Global Toggle

A global switch enables or disables Ponytail.

Values:

- enabled
- disabled

Default: disabled

---

## 5.2 Per Request Toggle

Clients may override the global setting.

OpenAI compatible:

```json
{
  "model": "zealish-router/agent-pool",
  "messages": [...],
  "ponytail": true
}
```

Supported values:

- true
- false

If omitted, global configuration is used.

---

## 5.3 Automatic Activation

Optimization only runs when the request exceeds configurable thresholds.

Conditions:

- input tokens ≥ threshold
- message count ≥ threshold
- attached documents exist
- duplicated context detected

Otherwise the request bypasses Ponytail.

---

## 5.4 Context Deduplication

Detect repeated content across messages.

Rules:

- remove exact duplicates
- merge consecutive identical system prompts
- collapse repeated assistant outputs
- preserve newest version

Never remove user messages.

---

## 5.5 Long Conversation Compression

Older conversation turns are compressed into concise summaries.

Requirements:

- preserve facts
- preserve decisions
- preserve code requirements
- preserve variable names
- preserve API contracts

Recent conversation remains untouched.

Compression occurs only for messages outside the protected window.

---

## 5.6 Code Context Compression

When source code exceeds limits:

- preserve imports
- preserve exports
- preserve function signatures
- preserve types
- preserve interfaces
- preserve comments marked IMPORTANT

Remove:

- repeated blank lines
- duplicated comments
- unchanged generated code
- formatting-only noise

No syntax modification.

---

## 5.7 Message Ranking

Assign relevance scores internally.

Priority:

1. System
2. Developer
3. Latest user
4. Active tool results
5. Recent assistant
6. Older conversation

Lowest ranked content becomes compression candidates.

Ranking is internal only.

---

## 5.8 Protected Window

Newest messages are never compressed.

Default:

- last 8 messages
- latest system prompt
- latest developer prompt

Configurable.

---

## 5.9 Token Budget

Target budget determines optimization aggressiveness.

Modes:

| Mode         | Behavior            |
| ------------ | ------------------- |
| conservative | Minimal compression |
| balanced     | Default             |
| aggressive   | Maximum reduction   |

Balanced is default.

---

## 5.10 Metadata

Router attaches optimization metadata.

Example:

```json
{
  "ponytail": {
    "enabled": true,
    "mode": "balanced",
    "saved_tokens": 18234,
    "compression_ratio": 0.41
  }
}
```

Metadata is optional.

---

# 6. Configuration

## Global Config

```yaml
ponytail:
  enabled: true
  mode: balanced

  thresholds:
    min_input_tokens: 12000
    min_messages: 16

  protected_window: 8

  compression:
    conversation: true
    code: true
    deduplicate: true

  metadata: true
```

---

# 7. Admin Dashboard

## Settings Page

Section:

Ponytail

Controls:

- Enable switch
- Mode selector
- Token threshold
- Message threshold
- Protected window
- Metadata switch

Buttons:

- Save
- Reset

---

## Analytics

Display:

- Requests optimized
- Average tokens saved
- Total tokens saved
- Average compression ratio
- Average latency improvement

Time ranges:

- 24H
- 7D
- 30D

---

# 8. API Compatibility

No breaking changes.

Supported endpoint:

```
POST /v1/chat/completions
```

Additional optional field:

```json
{
  "ponytail": true
}
```

Streaming behavior remains identical.

---

# 9. Internal Pipeline

1. Receive request
2. Validate API key
3. Select provider
4. Count tokens
5. Check Ponytail eligibility
6. Deduplicate
7. Rank messages
8. Compress older context
9. Recalculate tokens
10. Forward to provider
11. Stream response unchanged

---

# 10. Failure Handling

If Ponytail fails:

- log error
- bypass optimization
- continue request normally

User must never receive an optimization error.

---

# 11. Logging

Record:

- request id
- original tokens
- optimized tokens
- saved tokens
- mode
- execution time
- provider
- model

Never store API keys.

---

# 12. Success Metrics

| Metric                      | Target |
| --------------------------- | ------ |
| Token reduction             | 25–60% |
| Semantic preservation       | ≥95%   |
| Added preprocessing latency | ≤50 ms |
| API compatibility           | 100%   |
| Streaming compatibility     | 100%   |

---

# 13. Future Enhancements

- Repository-aware file ranking
- AST-level code compression
- Language-specific optimizers
- Prompt cache integration
- Cross-request memory optimization
- Provider-specific token budgeting
