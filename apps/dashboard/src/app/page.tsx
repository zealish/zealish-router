"use client";

import { useEffect, useRef, useState } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { Overview } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const POLL_MS = 5000;
const MAX_POINTS = 60;

type Point = { t: string; rps: number; streams: number };

export default function OverviewPage() {
  const { data, error, loading } = useResource<Overview>("/overview", POLL_MS);
  const [series, setSeries] = useState<Point[]>([]);
  const previous = useRef<{ requests: number; at: number } | undefined>(
    undefined,
  );

  useEffect(() => {
    if (!data) return;
    const now = Date.now();
    const last = previous.current;
    previous.current = { requests: data.requests, at: now };
    if (!last) return;

    const seconds = (now - last.at) / 1000;
    setSeries((prev) =>
      [
        ...prev,
        {
          t: new Date(now).toLocaleTimeString(),
          rps: seconds > 0 ? (data.requests - last.requests) / seconds : 0,
          streams: data.active_streams,
        },
      ].slice(-MAX_POINTS),
    );
  }, [data]);

  if (error) {
    return (
      <>
        <PageHeader title="Overview" />
        <p className="text-destructive text-sm">{error}</p>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title="Overview"
        description="Live counters read from the router's Prometheus registry."
      />

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Requests" value={data?.requests} loading={loading} />
        <Stat
          label="Error rate"
          value={data && `${(data.error_rate * 100).toFixed(1)}%`}
          loading={loading}
        />
        <Stat
          label="Active streams"
          value={data?.active_streams}
          loading={loading}
        />
        <Stat
          label="Provider errors"
          value={data?.errors}
          loading={loading}
        />
        <Stat label="Providers" value={data?.providers} loading={loading} />
        <Stat label="Models" value={data?.models} loading={loading} />
        <Stat label="API keys" value={data?.api_keys} loading={loading} />
      </div>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Throughput</CardTitle>
        </CardHeader>
        <CardContent className="h-72">
          {series.length < 2 ? (
            <div className="text-muted-foreground flex h-full items-center justify-center text-sm">
              Sampling every {POLL_MS / 1000}s…
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={series}>
                <CartesianGrid
                  strokeDasharray="3 3"
                  className="stroke-border"
                />
                <XAxis dataKey="t" fontSize={11} tickLine={false} />
                <YAxis fontSize={11} tickLine={false} width={40} />
                <Tooltip />
                <Area
                  type="monotone"
                  dataKey="rps"
                  name="req/s"
                  stroke="var(--color-primary)"
                  fill="var(--color-primary)"
                  fillOpacity={0.15}
                />
                <Area
                  type="monotone"
                  dataKey="streams"
                  name="streams"
                  stroke="var(--color-muted-foreground)"
                  fill="var(--color-muted-foreground)"
                  fillOpacity={0.1}
                />
              </AreaChart>
            </ResponsiveContainer>
          )}
        </CardContent>
      </Card>
    </>
  );
}

function Stat({
  label,
  value,
  loading,
}: {
  label: string;
  value: number | string | undefined;
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
            {typeof value === "number" ? Math.round(value) : (value ?? "—")}
          </span>
        )}
      </CardContent>
    </Card>
  );
}
