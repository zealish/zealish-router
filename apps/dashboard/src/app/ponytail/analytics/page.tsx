"use client";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { type PonytailAnalytics } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const POLL_MS = 10_000;

export default function PonytailAnalyticsPage() {
  const { data, error, loading } = useResource<PonytailAnalytics>(
    "/ponytail/analytics",
    POLL_MS,
  );

  const totalSavings =
    data != null ? data.total_original_tokens - data.total_optimized_tokens : undefined;

  return (
    <>
      <PageHeader
        title="Ponytail Analytics"
        description="Optimization performance metrics."
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Stat
          label="Requests optimized"
          value={data && formatInt(data.requests_optimized)}
          loading={loading}
        />
        <Stat
          label="Avg tokens saved"
          value={data && formatInt(data.avg_tokens_saved)}
          loading={loading}
        />
        <Stat
          label="Total tokens saved"
          value={data && formatInt(data.total_tokens_saved)}
          loading={loading}
        />
        <Stat
          label="Avg compression ratio"
          value={data && `${(data.avg_compression_ratio * 100).toFixed(1)}%`}
          loading={loading}
        />
        <Stat
          label="Avg latency"
          value={data && `${data.avg_latency_ms} ms`}
          loading={loading}
        />
        <Stat
          label="Total savings"
          value={totalSavings != null ? formatInt(totalSavings) : undefined}
          hint="Tokens eliminated"
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
