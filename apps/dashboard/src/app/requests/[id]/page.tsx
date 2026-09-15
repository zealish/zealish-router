"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ArrowLeft, CornerDownRight, RotateCw } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import {
  formatCost,
  formatLatency,
  StatusBadge,
} from "@/components/request-trace";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import type { RequestAttempt, RequestTrace } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

export default function RequestDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;
  const { data, error, loading, reload } = useResource<RequestTrace>(
    `/requests/${encodeURIComponent(id)}`,
  );

  const attempts = data?.attempts ?? [];
  // The timeline is drawn relative to the first attempt, so a bar's offset
  // shows when in the request it happened rather than an absolute clock time.
  const origin = attempts.length
    ? new Date(attempts[0].started_at).getTime()
    : 0;
  const span = Math.max(data?.total_latency_ms ?? 0, 1);

  return (
    <>
      <PageHeader
        title="Request detail"
        description={id}
        action={
          <div className="flex gap-2">
            <Button variant="outline" size="sm" asChild>
              <Link href="/requests">
                <ArrowLeft className="size-4" />
                Back
              </Link>
            </Button>
            <Button variant="outline" size="sm" onClick={() => void reload()}>
              <RotateCw className="size-4" />
              Refresh
            </Button>
          </div>
        }
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      {loading && !data ? (
        <Skeleton className="h-40 w-full" />
      ) : data ? (
        <>
          <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Summary label="Status">
              <StatusBadge status={data.final_status} />
            </Summary>
            <Summary label="Total latency">
              {formatLatency(data.total_latency_ms)}
            </Summary>
            <Summary label="Total tokens">
              {data.total_tokens.toLocaleString()}
            </Summary>
            <Summary label="Total cost">
              {formatCost(data.total_cost_usd)}
            </Summary>
          </div>

          <Card className="mb-6">
            <CardHeader>
              <CardTitle className="text-sm">Request</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-3 text-sm sm:grid-cols-2">
              <Field label="Requested model" value={data.model} mono />
              <Field
                label="Served by"
                value={
                  data.final_provider
                    ? `${data.final_provider} · ${data.final_alias}`
                    : "—"
                }
                mono
              />
              <Field
                label="Started"
                value={new Date(data.created_at).toLocaleString()}
              />
              <Field
                label="API key"
                value={data.api_key || "unattributed"}
                mono
              />
              <Field label="Mode" value={data.streamed ? "stream" : "unary"} />
              <Field label="Client dialect" value={data.dialect} mono />
              <Field label="Attempts" value={String(data.attempt_count)} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-sm">Attempt timeline</CardTitle>
            </CardHeader>
            <CardContent>
              {attempts.length === 0 ? (
                <p className="text-muted-foreground py-6 text-center text-sm">
                  This request never reached a provider.
                </p>
              ) : (
                <ol className="space-y-4">
                  {attempts.map((attempt, i) => (
                    <li key={attempt.seq}>
                      {i > 0 ? <Separator className="mb-4" /> : null}
                      <AttemptRow
                        attempt={attempt}
                        origin={origin}
                        span={span}
                      />
                    </li>
                  ))}
                </ol>
              )}
            </CardContent>
          </Card>
        </>
      ) : null}
    </>
  );
}

function AttemptRow({
  attempt,
  origin,
  span,
}: {
  attempt: RequestAttempt;
  origin: number;
  span: number;
}) {
  const offset = Math.max(0, new Date(attempt.started_at).getTime() - origin);
  // A skipped route has no latency of its own; it still gets a visible sliver
  // so the timeline never renders an invisible step.
  const width = Math.max((attempt.latency_ms / span) * 100, 1.5);
  const left = Math.min((offset / span) * 100, 100 - width);

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground w-6 text-xs tabular-nums">
          #{attempt.seq}
        </span>
        <span className="font-mono text-sm">{attempt.provider}</span>
        <span className="text-muted-foreground font-mono text-xs">
          {attempt.alias} → {attempt.model}
        </span>
        <StatusBadge status={attempt.status} />
        {attempt.fallback ? (
          <Badge variant="outline" className="gap-1">
            <CornerDownRight className="size-3" />
            fallback
          </Badge>
        ) : null}
        {attempt.retry ? <Badge variant="outline">retry</Badge> : null}
        <span className="text-muted-foreground ml-auto text-xs tabular-nums">
          {formatLatency(attempt.latency_ms)}
        </span>
      </div>

      <div className="bg-muted relative h-2 w-full overflow-hidden rounded-full">
        <div
          className={
            attempt.status === "ok"
              ? "bg-primary h-full"
              : "bg-destructive h-full"
          }
          style={{ marginLeft: `${left}%`, width: `${width}%` }}
        />
      </div>

      {attempt.error ? (
        <p className="text-muted-foreground font-mono text-xs break-all">
          {attempt.error}
        </p>
      ) : null}
    </div>
  );
}

function Summary({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
          {label}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <span className="font-heading text-xl font-semibold tabular-nums">
          {children}
        </span>
      </CardContent>
    </Card>
  );
}

function Field({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <span className="text-muted-foreground block text-xs">{label}</span>
      <span className={mono ? "font-mono text-xs break-all" : ""}>{value}</span>
    </div>
  );
}
