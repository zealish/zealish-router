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
  del: (path: string) => request<void>("DELETE", path),
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
  enabled: boolean;
};

export type ComboInput = {
  strategy: ComboStrategy;
  members: string[];
  enabled: boolean;
};

export type ComboStrategy = "fallback" | "round_robin";

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
};

export type CreatedApiKey = ApiKey & { key: string };

export type ProviderGroup = "custom" | "oauth" | "api_key";

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

export type ModelAlias = {
  alias: string;
  provider: string;
  model: string;
  fallback: string[];
};

export type ModelTestResult = {
  alias: string;
  provider: string;
  model: string;
  ok: boolean;
  latency_ms: number;
  error?: string;
};

export type CatalogModel = {
  id: string;
  owned_by?: string;
  imported: boolean;
  alias?: string;
};

export type ImportModelsResult = {
  imported: ModelAlias[];
  skipped: string[];
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
