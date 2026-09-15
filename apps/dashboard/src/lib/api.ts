"use client";

export const ROUTER_URL = (
  process.env.NEXT_PUBLIC_ROUTER_URL ?? "http://localhost:8787"
).replace(/\/$/, "");

const TOKEN_KEY = "zealish-admin-token";

export function getToken(): string {
  if (typeof window === "undefined") return "";
  return window.localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(token: string) {
  window.localStorage.setItem(TOKEN_KEY, token);
  window.dispatchEvent(new Event("zealish-token"));
}

export function clearToken() {
  window.localStorage.removeItem(TOKEN_KEY);
  window.dispatchEvent(new Event("zealish-token"));
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

type ErrorEnvelope = { error?: { message?: string } };

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${ROUTER_URL}/api/v1${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${getToken()}`,
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, `Cannot reach the router at ${ROUTER_URL}.`);
  }

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let payload: unknown = undefined;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = undefined;
    }
  }

  if (!res.ok) {
    const message =
      (payload as ErrorEnvelope | undefined)?.error?.message ??
      `Request failed with status ${res.status}.`;
    throw new ApiError(res.status, message);
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body: unknown) => request<T>("PUT", path, body),
  del: <T = void>(path: string) => request<T>("DELETE", path),
};

// --- resources, mirroring internal/api/admin.go ---

export type Overview = {
  requests: number;
  errors: number;
  error_rate: number;
  active_streams: number;
  providers: number;
  models: number;
  combos: number;
  api_keys: number;
  total_requests: number;
  prompt_tokens: number;
  cached_tokens: number;
  completion_tokens: number;
  cost_usd: number;
};

export type Combo = {
  name: string;
  strategy: ComboStrategy;
  members: string[];
  weights: number[];
  enabled: boolean;
};

export type ComboInput = {
  strategy: ComboStrategy;
  members: string[];
  weights: number[];
  enabled: boolean;
};

export type ComboStrategy =
  "fallback" | "round_robin" | "weighted" | "intelligent";

/** Strategies, mirroring storage.ComboStrategy in internal/storage/storage.go. */
export const COMBO_STRATEGIES: {
  id: ComboStrategy;
  label: string;
  description: string;
}[] = [
  {
    id: "fallback",
    label: "Fallback",
    description:
      "Always start at the first member and cascade down the pool on failure.",
  },
  {
    id: "round_robin",
    label: "Round-robin",
    description:
      "Rotate the starting member per request to spread quota, then cascade.",
  },
  {
    id: "weighted",
    label: "Weighted",
    description:
      "Rotate the starting member in proportion to its weight, then cascade.",
  },
  {
    id: "intelligent",
    label: "Intelligent",
    description:
      "Order the pool per request by live health, success rate and latency, then cascade.",
  },
];

export type UsageEvent = {
  id: number;
  created_at: string;
  alias: string;
  provider: string;
  model: string;
  streamed: boolean;
  status: string;
  duration_ms: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  cost_usd: number;
};

/**
 * Attempt outcomes, mirroring router.classify in internal/router/router.go
 * plus the two classes the chain assigns to routes it skipped without calling.
 */
export const REQUEST_STATUSES = [
  "ok",
  "timeout",
  "rate_limited",
  "upstream_5xx",
  "connection",
  "client_error",
  "canceled",
  "circuit_open",
  "unhealthy",
] as const;

export type RequestStatus = (typeof REQUEST_STATUSES)[number];

/**
 * The wire dialect a client spoke. OpenAI covers /v1/chat/completions and
 * /v1/embeddings; Anthropic covers /v1/messages. Both reach the same aliases,
 * so the dialect describes the caller, not the route.
 */
export const REQUEST_DIALECTS = ["openai", "anthropic"] as const;

export type RequestDialect = (typeof REQUEST_DIALECTS)[number];

/** One upstream call inside a trace, served by /requests/:id. */
export type RequestAttempt = {
  seq: number;
  started_at: string;
  alias: string;
  provider: string;
  model: string;
  latency_ms: number;
  status: string;
  /** A repeat of the same route. */
  retry: boolean;
  /** Reached by falling back off the route before it. */
  fallback: boolean;
  error?: string;
};

/**
 * One gateway request. The list view leaves `attempts` empty; the detail
 * endpoint fills it with the whole timeline.
 */
export type RequestTrace = {
  request_id: string;
  created_at: string;
  api_key: string;
  model: string;
  dialect: RequestDialect;
  streamed: boolean;
  total_latency_ms: number;
  total_tokens: number;
  total_cost_usd: number;
  final_provider: string;
  final_alias: string;
  final_status: string;
  attempt_count: number;
  attempts?: RequestAttempt[];
};

export type RequestList = {
  items: RequestTrace[];
  total: number;
  limit: number;
  offset: number;
};

export type ModelUsage = {
  alias: string;
  requests: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  total_tokens: number;
  cost_usd: number;
  last_used: string;
};

export type UsageBucket = {
  start: string;
  requests: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  cost_usd: number;
};

export type UsageSummary = {
  window_hours: number;
  bucket_ms: number;
  totals: {
    requests: number;
    errors: number;
    prompt_tokens: number;
    completion_tokens: number;
    cached_tokens: number;
    cost_usd: number;
  };
  series: UsageBucket[];
};

export type ApiKey = {
  id: string;
  name: string;
  enabled: boolean;
  created_at: string;
  last_used_at: string | null;
  /** 0 means unlimited. */
  rate_limit_per_min: number;
  /** 0 means unlimited. */
  monthly_budget_usd: number;
  /** Empty or absent means every model is reachable. */
  allowed_models: string[] | null;
  month_spend_usd: number;
  requests: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
};

export type CreatedApiKey = ApiKey & { key: string };

export type ProviderGroup = "custom" | "oauth" | "api_key";

/** Breaker phase, mirroring router.CircuitState in internal/router/breaker.go. */
export type CircuitState = "closed" | "open" | "half_open";

export type Provider = {
  name: string;
  group: ProviderGroup;
  catalog_id?: string;
  kind: string;
  base_url: string;
  has_api_key: boolean;
  timeout_ms: number;
  enabled: boolean;
  alias_prefix: string;
  use_proxy_pool: boolean;
  circuit?: CircuitState;
  circuit_retry_at?: string;
  /** Null means the provider inherits the policy from config.yaml. */
  breaker_threshold?: number | null;
  breaker_cooldown_ms?: number | null;
};

export type ProviderInput = {
  group: ProviderGroup;
  catalog_id?: string;
  kind: string;
  base_url: string;
  api_key?: string;
  timeout_ms: number;
  enabled: boolean;
  alias_prefix: string;
  use_proxy_pool?: boolean;
  /** Null clears the override, returning the provider to the global policy. */
  breaker_threshold?: number | null;
  breaker_cooldown_ms?: number | null;
};

export type Proxy = {
  name: string;
  url: string;
  enabled: boolean;
};

export type ProxyInput = {
  url: string;
  enabled: boolean;
};

export type ImportProxiesResult = {
  imported: Proxy[];
  skipped: string[];
  invalid: string[];
};

/** A preset offered when adding a provider, served by /provider-catalog. */
export type CatalogEntry = {
  id: string;
  label: string;
  group: ProviderGroup;
  kind: string;
  base_url: string;
  alias_prefix: string;
  docs?: string;
};

/** Capability ids, mirroring internal/provider/capability.go. */
export type Capability =
  | "chat"
  | "vision"
  | "tools"
  | "embeddings"
  | "reasoning"
  | "streaming"
  | "audio"
  | "json_mode";

/** One entry of the capability vocabulary, served by /capabilities. */
export type CapabilityInfo = {
  id: Capability;
  label: string;
  description: string;
};

export type ModelAlias = {
  alias: string;
  provider: string;
  model: string;
  fallback: string[];
  capabilities: Capability[];
};

export type ModelTestResult = {
  alias: string;
  provider: string;
  model: string;
  ok: boolean;
  latency_ms: number;
  ttfb_ms: number;
  error?: string;
};

/** Sample size below which a tail percentile is withheld by the router. */
export const MIN_CONFIDENT_SAMPLES = 10;

export type Confidence = "low" | "high";

/**
 * Rolling request-window stats, served by /providers/:name/metrics. `p95_ms`
 * is null until the window holds enough samples to estimate a tail.
 */
export type AliasMetrics = {
  alias: string;
  ttfb_ms: number;
  p50_ms: number;
  p95_ms: number | null;
  success_rate: number;
  requests: number;
  confidence: Confidence;
  updated_at: string;
};

/** The provider's aliases pooled over every request, not averaged. */
export type MetricsSummary = {
  ttfb_ms: number;
  p50_ms: number;
  p95_ms: number | null;
  success_rate: number;
  requests: number;
  confidence: Confidence;
};

export type ProviderMetrics = {
  aliases: AliasMetrics[];
  summary: MetricsSummary | null;
};

export type CatalogModel = {
  id: string;
  owned_by?: string;
  imported: boolean;
  alias?: string;
  /** What importing this model would tag it with. */
  capabilities: Capability[];
};

export type ImportModelsResult = {
  imported: ModelAlias[];
  skipped: string[];
};

/** Response cache stats, served by /cache. */
export type CacheStats = {
  entries: number;
  capacity: number;
  hits: number;
  misses: number;
  stores: number;
  evictions: number;
  enabled: boolean;
  /** Entry lifetime as a Go duration string, "0s" when the cache is off. */
  ttl: string;
  hit_rate: number;
};

export type Settings = Record<string, string>;

/** Wire dialects, mirroring provider.Kinds in internal/provider/catalog.go. */
export const PROVIDER_KINDS = ["openai", "anthropic"] as const;

/** Groups, mirroring provider.Groups in internal/provider/catalog.go. */
export const PROVIDER_GROUPS: {
  id: ProviderGroup;
  label: string;
  description: string;
}[] = [
  {
    id: "custom",
    label: "Custom provider",
    description:
      "Any OpenAI- or Anthropic-compatible endpoint you configure by hand.",
  },
  {
    id: "oauth",
    label: "OAuth provider",
    description:
      "Upstream authenticated with an OAuth access token, sent as a bearer credential.",
  },
  {
    id: "api_key",
    label: "API key provider",
    description: "Known upstream from the catalogue, authenticated with a key.",
  },
];
