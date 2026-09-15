"use client";

import { useState } from "react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { api, ApiError, type CacheStats } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const POLL_MS = 5000;

export default function CachePage() {
  const { data, error, loading, reload } = useResource<CacheStats>(
    "/cache",
    POLL_MS,
  );
  const [purging, setPurging] = useState(false);

  const purge = async () => {
    setPurging(true);
    try {
      const { purged } = await api.del<{ purged: number }>("/cache");
      toast.success(`Purged ${purged} ${purged === 1 ? "entry" : "entries"}.`);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setPurging(false);
    }
  };

  const lookups = data ? data.hits + data.misses : 0;
  const fill = data?.capacity ? data.entries / data.capacity : 0;

  return (
    <>
      <PageHeader
        title="Response cache"
        description="Exact-match cache of non-streaming chat and embeddings responses."
        action={
          <div className="flex items-center gap-3">
            {data ? (
              data.enabled ? (
                <Badge>enabled</Badge>
              ) : (
                <Badge variant="outline">disabled</Badge>
              )
            ) : null}
            <Button
              variant="destructive"
              disabled={purging || !data?.entries}
              onClick={purge}
            >
              {purging ? "Purging…" : "Purge"}
            </Button>
          </div>
        }
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      {data && !data.enabled ? (
        <p className="text-muted-foreground mb-6 rounded-xl border border-dashed p-4 text-sm">
          Caching is off. Set <code className="font-mono">cache.enabled</code>{" "}
          along with a TTL and <code className="font-mono">max_entries</code> in{" "}
          <code className="font-mono">config.yaml</code> to turn it on; the
          router picks the change up without a restart.
        </p>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat
          label="Hit rate"
          value={data && `${(data.hit_rate * 100).toFixed(1)}%`}
          hint={data && `${formatInt(lookups)} lookups`}
          loading={loading}
        />
        <Stat
          label="Entries"
          value={data && formatInt(data.entries)}
          hint={
            data &&
            `of ${formatInt(data.capacity)} (${Math.round(fill * 100)}% full)`
          }
          loading={loading}
        />
        <Stat
          label="TTL"
          value={data?.ttl}
          hint="Per-entry lifetime"
          loading={loading}
        />
        <Stat
          label="Evictions"
          value={data && formatInt(data.evictions)}
          hint="Dropped to stay under capacity"
          loading={loading}
        />
        <Stat
          label="Hits"
          value={data && formatInt(data.hits)}
          hint="Served without an upstream call"
          loading={loading}
        />
        <Stat
          label="Misses"
          value={data && formatInt(data.misses)}
          hint="Forwarded to a provider"
          loading={loading}
        />
        <Stat
          label="Stores"
          value={data && formatInt(data.stores)}
          hint="Responses admitted to the cache"
          loading={loading}
        />
      </div>
    </>
  );
}

function Stat({
  label,
  value,
  hint,
  loading,
}: {
  label: string;
  value: string | undefined;
  hint?: string;
  loading: boolean;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
          {label}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {loading && value === undefined ? (
          <Skeleton className="h-7 w-20" />
        ) : (
          <span className="font-heading text-2xl font-semibold tabular-nums">
            {value ?? "—"}
          </span>
        )}
        {hint ? (
          <p className="text-muted-foreground mt-1 text-xs">{hint}</p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function formatInt(n: number): string {
  return Math.round(n).toLocaleString();
}
