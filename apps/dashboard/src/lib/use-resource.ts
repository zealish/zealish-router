"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";

type Resource<T> = {
  data: T | undefined;
  error: string | undefined;
  loading: boolean;
  reload: () => Promise<void>;
};

/** Fetches an admin endpoint, optionally re-polling every `intervalMs`. */
export function useResource<T>(path: string, intervalMs?: number): Resource<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);

  const reload = useCallback(async () => {
    try {
      setData(await api.get<T>(path));
      setError(undefined);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    // Deferred so the fetch never triggers a setState during the effect pass.
    const initial = setTimeout(() => void reload(), 0);
    const poll = intervalMs
      ? setInterval(() => void reload(), intervalMs)
      : undefined;
    return () => {
      clearTimeout(initial);
      clearInterval(poll);
    };
  }, [reload, intervalMs]);

  return { data, error, loading, reload };
}
