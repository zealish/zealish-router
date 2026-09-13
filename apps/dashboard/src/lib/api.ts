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
  api_keys: number;
};

export type ApiKey = {
  id: string;
  name: string;
  enabled: boolean;
  created_at: string;
  last_used_at: string | null;
};

export type CreatedApiKey = ApiKey & { key: string };

export type Provider = {
  name: string;
  kind: string;
  base_url: string;
  has_api_key: boolean;
  timeout_ms: number;
  enabled: boolean;
};

export type ProviderInput = {
  kind: string;
  base_url: string;
  api_key?: string;
  timeout_ms: number;
  enabled: boolean;
};

export type ModelAlias = {
  alias: string;
  provider: string;
  model: string;
  fallback: string[];
};

export type Settings = Record<string, string>;

export const PROVIDER_KINDS = ["openai", "openrouter", "ollama"] as const;
