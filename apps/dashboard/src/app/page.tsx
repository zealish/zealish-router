"use client";

import Image from "next/image";
import { useState } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { DataTable, DataTableColumnHeader } from "@/components/data-table";
import type { ColumnDef } from "@tanstack/react-table";
import type {
  ActiveRequest,
  ModelUsage,
  Overview,
  UsageEvent,
  UsageSummary,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const POLL_MS = 5000;
const WINDOWS = [
  { label: "1h", hours: 1 },
  { label: "24h", hours: 24 },
  { label: "7d", hours: 168 },
  { label: "30d", hours: 720 },
];
const LB_SORTS = [
  { label: "Requests", value: "requests" },
  { label: "Tokens", value: "tokens" },
  { label: "Cost", value: "cost" },
] as const;

type LbSort = (typeof LB_SORTS)[number]["value"];

const AXIS_PROPS = {
  stroke: "var(--color-muted-foreground)",
  tick: { fill: "var(--color-muted-foreground)" },
} as const;

const TOOLTIP_PROPS = {
  cursor: { stroke: "var(--color-border)" },
  contentStyle: {
    background: "var(--color-popover)",
    border: "1px solid var(--color-border)",
    borderRadius: "var(--radius-md)",
    color: "var(--color-popover-foreground)",
    fontSize: 12,
  },
  labelStyle: { color: "var(--color-foreground)" },
  itemStyle: { color: "var(--color-popover-foreground)" },
} as const;

const recentColumns: ColumnDef<UsageEvent, unknown>[] = [
  {
    accessorKey: "created_at",
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title="Time" />
    ),
    cell: ({ row }) => (
      <span className="text-muted-foreground whitespace-nowrap">
        {new Date(row.original.created_at).toLocaleTimeString()}
      </span>
    ),
  },
  {
    accessorKey: "alias",
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title="Model" />
    ),
    cell: ({ row }) => (
      <span className="font-mono text-xs">
        {row.original.alias}
        {row.original.streamed ? (
          <span className="text-muted-foreground ml-2">stream</span>
        ) : null}
      </span>
    ),
  },
  {
    accessorKey: "prompt_tokens",
    meta: { className: "text-right" },
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title="Input" className="ml-auto" />
    ),
    cell: ({ row }) => (
      <span className="tabular-nums">
        {formatInt(row.original.prompt_tokens)}
      </span>
    ),
  },
  {
    accessorKey: "cached_tokens",
    meta: { className: "text-right" },
    header: ({ column }) => (
      <DataTableColumnHeader
        column={column}
        title="Cached"
        className="ml-auto"
      />
    ),
    cell: ({ row }) => (
      <span className="text-muted-foreground tabular-nums">
        {row.original.cached_tokens ? formatInt(row.original.cached_tokens) : "—"}
      </span>
    ),
  },
  {
    accessorKey: "completion_tokens",
    meta: { className: "text-right" },
    header: ({ column }) => (
      <DataTableColumnHeader
        column={column}
        title="Output"
        className="ml-auto"
      />
    ),
    cell: ({ row }) => (
      <span className="tabular-nums">
        {formatInt(row.original.completion_tokens)}
      </span>
    ),
  },
  {
    accessorKey: "duration_ms",
    meta: { className: "text-right" },
    header: ({ column }) => (
      <DataTableColumnHeader
        column={column}
        title="Duration"
        className="ml-auto"
      />
    ),
    cell: ({ row }) => (
      <span className="text-muted-foreground tabular-nums">
        {(row.original.duration_ms / 1000).toFixed(2)}s
      </span>
    ),
  },
  {
    accessorKey: "cost_usd",
    meta: { className: "text-right" },
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title="Cost" className="ml-auto" />
    ),
    cell: ({ row }) => (
      <span className="tabular-nums">{formatCost(row.original.cost_usd)}</span>
    ),
  },
];

export default function OverviewPage() {
  const [hours, setHours] = useState(24);
  const [statHours, setStatHours] = useState(0);
  const [lbHours, setLbHours] = useState(24);
  const [lbSort, setLbSort] = useState<LbSort>("requests");
  const active = useResource<ActiveRequest[]>("/requests/active", 1000);
  const overview = useResource<Overview>(
    statHours ? `/overview?hours=${statHours}` : "/overview",
    POLL_MS,
  );
  const usage = useResource<UsageSummary>(`/usage?hours=${hours}`, POLL_MS);
  const recent = useResource<UsageEvent[]>("/usage/recent?limit=15", POLL_MS);
  const models = useResource<ModelUsage[]>("/usage/models", POLL_MS);
  const leaderboard = useResource<ModelUsage[]>(
    `/usage/leaderboard?hours=${lbHours}&limit=10&sort=${lbSort}`,
    POLL_MS,
  );

  const error =
    active.error ??
    overview.error ??
    usage.error ??
    recent.error ??
    models.error ??
    leaderboard.error;
  if (error) {
    return (
      <>
        <PageHeader title="Overview" />
        <p className="text-destructive text-sm">{error}</p>
      </>
    );
  }

  const data = overview.data;
  const loading = overview.loading;
  const lbKey =
    lbSort === "tokens"
      ? "total_tokens"
      : lbSort === "cost"
        ? "cost_usd"
        : "requests";
  const lbTop = leaderboard.data?.length ? leaderboard.data[0][lbKey] || 1 : 1;

  return (
    <>
      <PageHeader
        title="Overview"
        description="Usage totals and cost from the router's request log."
      />

      <div className="mb-4 flex justify-end gap-1">
        <Button
          size="sm"
          variant={statHours === 0 ? "default" : "outline"}
          onClick={() => setStatHours(0)}
        >
          All
        </Button>
        {WINDOWS.map((w) => (
          <Button
            key={w.hours}
            size="sm"
            variant={statHours === w.hours ? "default" : "outline"}
            onClick={() => setStatHours(w.hours)}
          >
            {w.label}
          </Button>
        ))}
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
        <Stat
          label="Total requests"
          value={data && formatInt(data.total_requests)}
          loading={loading}
        />
        <Stat
          label="Input tokens"
          value={data && formatTokens(data.prompt_tokens)}
          loading={loading}
        />
        <Stat
          label="Cached tokens"
          value={data && formatTokens(data.cached_tokens)}
          loading={loading}
        />
        <Stat
          label="Output tokens"
          value={data && formatTokens(data.completion_tokens)}
          loading={loading}
        />
        <Stat
          label="Total cost"
          value={data && formatCost(data.cost_usd)}
          loading={loading}
        />
      </div>

      <RequestFlow active={active.data ?? []} latest={recent.data?.[0]} />

      <div className="mt-6 grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader className="flex flex-row items-center justify-between gap-4 space-y-0">
            <CardTitle>Request activity</CardTitle>
            <div className="flex gap-1">
              {WINDOWS.map((w) => (
                <Button
                  key={w.hours}
                  size="sm"
                  variant={hours === w.hours ? "default" : "outline"}
                  onClick={() => setHours(w.hours)}
                >
                  {w.label}
                </Button>
              ))}
            </div>
          </CardHeader>
          <CardContent>
            <Tabs defaultValue="tokens">
              <TabsList>
                <TabsTrigger value="tokens">Tokens</TabsTrigger>
                <TabsTrigger value="cost">Cost</TabsTrigger>
              </TabsList>

              <TabsContent value="tokens" className="h-72 flex-none">
                <ChartFrame
                  empty={!usage.data?.series.length}
                  loading={usage.loading}
                >
                  <AreaChart data={usage.data?.series ?? []}>
                    <CartesianGrid
                      strokeDasharray="3 3"
                      className="stroke-border"
                    />
                    <XAxis
                      dataKey="start"
                      fontSize={11}
                      tickLine={false}
                      tickFormatter={(v) => formatTime(v, hours)}
                      {...AXIS_PROPS}
                    />
                    <YAxis
                      fontSize={11}
                      tickLine={false}
                      width={48}
                      tickFormatter={formatTokens}
                      {...AXIS_PROPS}
                    />
                    <Tooltip
                      labelFormatter={(v) => formatTime(String(v), hours)}
                      formatter={(value) => formatInt(Number(value))}
                      {...TOOLTIP_PROPS}
                    />
                    <Legend
                      wrapperStyle={{
                        color: "var(--color-muted-foreground)",
                        fontSize: 12,
                      }}
                    />
                    <Area
                      type="monotone"
                      dataKey="prompt_tokens"
                      name="Input"
                      stackId="1"
                      stroke="var(--color-primary)"
                      fill="var(--color-primary)"
                      fillOpacity={0.2}
                    />
                    <Area
                      type="monotone"
                      dataKey="completion_tokens"
                      name="Output"
                      stackId="1"
                      stroke="var(--color-chart-2, var(--color-muted-foreground))"
                      fill="var(--color-chart-2, var(--color-muted-foreground))"
                      fillOpacity={0.2}
                    />
                  </AreaChart>
                </ChartFrame>
              </TabsContent>

              <TabsContent value="cost" className="h-72 flex-none">
                <ChartFrame
                  empty={!usage.data?.series.length}
                  loading={usage.loading}
                >
                  <AreaChart data={usage.data?.series ?? []}>
                    <CartesianGrid
                      strokeDasharray="3 3"
                      className="stroke-border"
                    />
                    <XAxis
                      dataKey="start"
                      fontSize={11}
                      tickLine={false}
                      tickFormatter={(v) => formatTime(v, hours)}
                      {...AXIS_PROPS}
                    />
                    <YAxis
                      fontSize={11}
                      tickLine={false}
                      width={64}
                      tickFormatter={(v) => formatCost(v)}
                      {...AXIS_PROPS}
                    />
                    <Tooltip
                      labelFormatter={(v) => formatTime(String(v), hours)}
                      formatter={(value) => [formatCost(Number(value)), "Cost"]}
                      {...TOOLTIP_PROPS}
                    />
                    <Area
                      type="monotone"
                      dataKey="cost_usd"
                      name="Cost"
                      stroke="var(--color-primary)"
                      fill="var(--color-primary)"
                      fillOpacity={0.15}
                    />
                  </AreaChart>
                </ChartFrame>
              </TabsContent>
            </Tabs>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Recent models</CardTitle>
          </CardHeader>
          <CardContent>
            {!models.data?.length ? (
              <p className="text-muted-foreground py-6 text-center text-sm">
                {models.loading ? "Loading…" : "No requests yet."}
              </p>
            ) : (
              <ul className="space-y-3">
                {models.data.map((m) => (
                  <li key={m.alias} className="flex items-center gap-3">
                    <div className="min-w-0 flex-1">
                      <p className="truncate font-mono text-xs">{m.alias}</p>
                      <p className="text-muted-foreground text-xs">
                        {m.requests} {m.requests === 1 ? "request" : "requests"}{" "}
                        · {formatTokens(m.total_tokens)} tokens
                      </p>
                    </div>
                    <span className="text-xs tabular-nums">
                      {formatCost(m.cost_usd)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Recent requests</CardTitle>
        </CardHeader>
        <CardContent>
          {recent.loading && !recent.data?.length ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              Loading…
            </p>
          ) : (
            <DataTable
              columns={recentColumns}
              data={recent.data ?? []}
              empty="No requests yet."
              searchKey="alias"
              searchPlaceholder="Search models…"
              enableExport
              exportFilename="recent-requests"
            />
          )}
        </CardContent>
      </Card>

      <div className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat
          label="Active streams"
          value={data?.active_streams}
          loading={loading}
        />
        <Stat label="Providers" value={data?.providers} loading={loading} />
        <Stat label="Models" value={data?.models} loading={loading} />
        <Stat label="API keys" value={data?.api_keys} loading={loading} />
      </div>

      <Card className="mt-6">
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-4 space-y-0">
          <CardTitle>Model leaderboard</CardTitle>
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex gap-1">
              {LB_SORTS.map((s) => (
                <Button
                  key={s.value}
                  size="sm"
                  variant={lbSort === s.value ? "default" : "outline"}
                  onClick={() => setLbSort(s.value)}
                >
                  {s.label}
                </Button>
              ))}
            </div>
            <div className="flex gap-1">
              {WINDOWS.map((w) => (
                <Button
                  key={w.hours}
                  size="sm"
                  variant={lbHours === w.hours ? "default" : "outline"}
                  onClick={() => setLbHours(w.hours)}
                >
                  {w.label}
                </Button>
              ))}
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {!leaderboard.data?.length ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              {leaderboard.loading ? "Loading…" : "No requests yet."}
            </p>
          ) : (
            <ol className="space-y-3">
              {leaderboard.data.map((m, i) => {
                return (
                  <li key={m.alias} className="flex items-center gap-3">
                    <span
                      className={`w-6 text-center text-sm font-semibold tabular-nums ${
                        i < 3 ? "text-primary" : "text-muted-foreground"
                      }`}
                    >
                      {i + 1}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-baseline justify-between gap-3">
                        <p className="truncate font-mono text-xs">{m.alias}</p>
                        <p className="text-muted-foreground shrink-0 text-xs tabular-nums">
                          {formatInt(m.requests)}{" "}
                          {m.requests === 1 ? "request" : "requests"} ·{" "}
                          {formatTokens(m.total_tokens)} tokens ·{" "}
                          {formatCost(m.cost_usd)}
                        </p>
                      </div>
                      <div className="bg-muted mt-1.5 h-1.5 overflow-hidden rounded-full">
                        <div
                          className="bg-primary h-full rounded-full"
                          style={{
                            width: `${Math.max((m[lbKey] / lbTop) * 100, 2)}%`,
                          }}
                        />
                      </div>
                    </div>
                  </li>
                );
              })}
            </ol>
          )}
        </CardContent>
      </Card>
    </>
  );
}

function RequestFlow({
  active,
  latest,
}: {
  active: ActiveRequest[];
  latest?: UsageEvent;
}) {
  const request = active[0];
  const apiKey = request?.api_key ?? "endpoint-api-key";
  const maskedKey = apiKey.length > 18 ? `${apiKey.slice(0, 8)}…${apiKey.slice(-6)}` : apiKey;
  const model = request?.model ?? latest?.alias ?? "waiting for a model";
  const isActive = active.length > 0;

  return (
    <Card className="relative mt-6 overflow-hidden border-primary/20 bg-linear-to-br from-primary/[0.06] via-card to-card">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_50%_0%,var(--color-primary)/0.12,transparent_48%)]" />
      <CardHeader className="relative flex flex-row items-start justify-between gap-4 space-y-0">
        <div>
          <CardTitle>Live request flow</CardTitle>
          <p className="text-muted-foreground mt-1 text-sm">API key → Zealish Router → model</p>
        </div>
        <span className="text-muted-foreground inline-flex items-center gap-2 text-xs">
          <span className={`size-2 rounded-full ${isActive ? "animate-pulse bg-emerald-500" : "bg-primary"}`} />
          {isActive ? `${active.length} active` : "waiting"}
        </span>
      </CardHeader>
      <CardContent className="relative">
        <div className="grid items-center gap-3 md:grid-cols-[1fr_auto_1fr_auto_1fr]">
          <FlowNode icon={<KeyIcon />} label="API key" value={maskedKey} />
          <FlowConnector active={isActive} />
          <FlowNode icon={<Image src="/logo.webp" alt="" width={36} height={36} className="size-9 object-contain" />} label="Router" value="zealish-router" accent />
          <FlowConnector active={isActive} reverse />
          <FlowNode icon={<ModelIcon />} label="Model used" value={model} />
        </div>
        <div className="text-muted-foreground mt-4 flex flex-wrap gap-x-3 gap-y-1 text-xs">
          <span>{request ? "Request in progress" : "No request in progress"}</span>
          <span className="hidden sm:inline">•</span>
          <span>{request ? new Date(request.started_at).toLocaleTimeString() : "Latest activity shown when idle"}</span>
        </div>
      </CardContent>
    </Card>
  );
}

function FlowNode({
  icon,
  label,
  value,
  accent = false,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  accent?: boolean;
}) {
  return (
    <div className={`min-w-0 rounded-xl border p-3 ${accent ? "border-primary/40 bg-primary/[0.08]" : "bg-background/60"}`}>
      <div className="flex items-center gap-3">
        <div className="text-primary flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">{icon}</div>
        <div className="min-w-0">
          <p className="text-muted-foreground text-[11px] uppercase tracking-wider">{label}</p>
          <p className="truncate font-mono text-xs">{value}</p>
        </div>
      </div>
    </div>
  );
}

function FlowConnector({ active, reverse = false }: { active: boolean; reverse?: boolean }) {
  return (
    <div className="relative hidden h-1 min-w-12 md:block">
      <div className="bg-border absolute inset-x-0 top-1/2 h-px" />
      <div className={`absolute top-1/2 size-2 -translate-y-1/2 rounded-full bg-primary ${reverse ? "animate-[flow-reverse_1.5s_linear_infinite]" : "animate-[flow_1.5s_linear_infinite]"} ${active ? "opacity-100" : "opacity-40"}`} />
    </div>
  );
}

function KeyIcon() {
  return <span className="font-mono text-lg font-bold">#</span>;
}

function ModelIcon() {
  return <span className="text-lg">✦</span>;
}

function ChartFrame({
  empty,
  loading,
  children,
}: {
  empty: boolean;
  loading: boolean;
  children: React.ReactElement;
}) {
  if (empty) {
    return (
      <div className="text-muted-foreground flex h-full items-center justify-center text-sm">
        {loading ? "Loading…" : "No requests in this window."}
      </div>
    );
  }
  return (
    <ResponsiveContainer width="100%" height="100%">
      {children}
    </ResponsiveContainer>
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

function formatInt(n: number): string {
  return Math.round(n).toLocaleString();
}

/** Compact token counts so axis labels stay narrow. */
function formatTokens(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return String(Math.round(n));
}

/** Costs are often fractions of a cent, so small values keep more digits. */
function formatCost(n: number): string {
  if (n === 0) return "$0.00";
  if (n < 0.01) return `$${n.toFixed(5)}`;
  if (n < 1) return `$${n.toFixed(4)}`;
  return `$${n.toFixed(2)}`;
}

/** Short windows label by time of day; long ones by date. */
function formatTime(iso: string, hours: number): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return hours > 48
    ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
    : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}
