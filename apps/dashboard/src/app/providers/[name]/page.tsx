"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import {
  Activity,
  ArrowLeft,
  Download,
  Loader2,
  Pencil,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import type { ColumnDef } from "@tanstack/react-table";
import {
  DataTable,
  DataTableColumnHeader,
  DataTableRowActions,
} from "@/components/data-table";
import { ImportModelsDialog } from "@/components/import-models-dialog";
import {
  CAPABILITIES,
  CapabilityBadges,
} from "@/components/capability-badges";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import {
  api,
  ApiError,
  type AliasMetrics,
  type Capability,
  type Confidence,
  MIN_CONFIDENT_SAMPLES,
  type ModelAlias,
  PROVIDER_GROUPS,
  type ModelTestResult,
  type Provider,
  type ProviderMetrics,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

/** Health metrics refresh on their own, so the page reflects live traffic. */
const METRICS_POLL_MS = 10_000;

const LOW_SAMPLE_HINT = `Need at least ${MIN_CONFIDENT_SAMPLES} completed requests before P95 becomes statistically meaningful.`;

/** Formats a latency in the unit that keeps it readable: ms under a second. */
function formatMs(ms: number): string {
  if (ms <= 0) return "—";
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(2)} s`;
}

function formatRate(rate: number): string {
  return `${rate.toFixed(1)}%`;
}

function formatCount(n: number): string {
  return n.toLocaleString();
}

/** Success-rate bands: anything under 95% is losing real traffic. */
function successTone(rate: number, confidence: Confidence): string {
  if (confidence === "low") return "";
  if (rate >= 99) return "text-emerald-600 dark:text-emerald-400";
  if (rate >= 95) return "text-amber-600 dark:text-amber-400";
  return "text-red-600 dark:text-red-400";
}

/**
 * P95 bands: a tail past 1.5s is where callers start timing out. A withheld
 * tail gets no colour at all — a small sample is unknown, not unhealthy.
 */
function latencyTone(ms: number | null): string {
  if (ms === null) return "";
  if (ms < 800) return "text-emerald-600 dark:text-emerald-400";
  if (ms <= 1500) return "text-amber-600 dark:text-amber-400";
  return "text-red-600 dark:text-red-400";
}

type Draft = {
  alias: string;
  model: string;
  fallback: string;
  capabilities: Capability[];
};

/**
 * CircuitDot is the compact health indicator for the model list: red while
 * the provider's circuit is open (the router skips it), green otherwise.
 */
function CircuitDot({ circuit }: { circuit?: string }) {
  const open = circuit === "open";
  return (
    <span
      title={
        open
          ? "Provider circuit open — requests are routed around this provider"
          : "Provider healthy"
      }
      className={`size-2 shrink-0 rounded-full ${
        open ? "bg-red-500" : "bg-emerald-500"
      }`}
    />
  );
}

export default function ProviderDetailPage() {
  const params = useParams<{ name: string }>();
  const name = decodeURIComponent(params.name);

  const providers = useResource<Provider[]>("/providers");
  const { data, error, reload } = useResource<ModelAlias[]>("/models");
  const metrics = useResource<ProviderMetrics>(
    `/providers/${encodeURIComponent(name)}/metrics`,
    METRICS_POLL_MS,
  );
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [importing, setImporting] = useState(false);
  // A probe's own numbers land in the rolling window; the table reads that,
  // so only the in-flight alias needs local state.
  const [testing, setTesting] = useState<string>();
  const [testingAll, setTestingAll] = useState(false);

  const provider = providers.data?.find((p) => p.name === name);
  const models = (data ?? []).filter((m) => m.provider === name);

  const stats: Record<string, AliasMetrics> = {};
  for (const row of metrics.data?.aliases ?? []) {
    stats[row.alias] = row;
  }

  // The summary is pooled over every request server-side, so a low-traffic
  // alias cannot weigh as much as one carrying the load.
  const summary = metrics.data?.summary ?? null;
  const caption = summary
    ? `${formatCount(summary.requests)} request${summary.requests === 1 ? "" : "s"}`
    : undefined;

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await api.put(`/models/${encodeURIComponent(draft.alias)}`, {
        provider: name,
        model: draft.model,
        fallback: draft.fallback
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        capabilities: draft.capabilities,
      });
      toast.success(`Saved alias '${draft.alias}'.`);
      setDraft(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (alias: string) => {
    if (!confirm(`Delete alias '${alias}'?`)) return;
    try {
      await api.del(`/models/${encodeURIComponent(alias)}`);
      toast.success(`Deleted alias '${alias}'.`);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    }
  };

  // Probes run one at a time: a provider under test should not be hit with the
  // whole alias list at once, and serial results keep the latency honest.
  const probe = async (alias: string) => {
    setTesting(alias);
    try {
      const result = await api.post<ModelTestResult>(
        `/models/${encodeURIComponent(alias)}/test`,
        {},
      );
      return result;
    } finally {
      setTesting(undefined);
    }
  };

  const runTest = async (alias: string) => {
    try {
      const result = await probe(alias);
      if (result.ok) {
        toast.success(`'${alias}' replied in ${result.latency_ms} ms.`);
      } else {
        toast.error(result.error ?? `'${alias}' failed.`);
      }
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      // The probe joined the rolling window; pull the new aggregate.
      await metrics.reload();
    }
  };

  const runAllTests = async () => {
    setTestingAll(true);
    let passed = 0;
    try {
      for (const model of models) {
        const result = await probe(model.alias);
        if (result.ok) passed++;
      }
      const failed = models.length - passed;
      if (failed === 0) {
        toast.success(`All ${passed} aliases replied.`);
      } else {
        toast.error(`${failed} of ${models.length} aliases failed.`);
      }
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setTestingAll(false);
      await metrics.reload();
    }
  };

  const columns: ColumnDef<ModelAlias, unknown>[] = [
    {
      accessorKey: "alias",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Alias" />
      ),
      cell: ({ row }) => (
        <span className="flex items-center gap-2">
          <CircuitDot circuit={provider?.circuit} />
          <span className="font-mono text-xs">{row.original.alias}</span>
        </span>
      ),
    },
    {
      accessorKey: "model",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Upstream model" />
      ),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.model}</span>
      ),
    },
    {
      id: "fallback",
      accessorFn: (model) => model.fallback.join(", "),
      enableSorting: false,
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Fallback chain" />
      ),
      cell: ({ row }) =>
        row.original.fallback.length === 0 ? (
          <span className="text-muted-foreground text-xs">none</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {row.original.fallback.map((alias) => (
              <Badge key={alias} variant="secondary">
                {alias}
              </Badge>
            ))}
          </div>
        ),
    },
    {
      id: "capabilities",
      accessorFn: (model) => model.capabilities.join(", "),
      enableSorting: false,
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Capabilities" />
      ),
      cell: ({ row }) => (
        <CapabilityBadges capabilities={row.original.capabilities} compact />
      ),
    },
    {
      id: "ttfb",
      accessorFn: (model) => stats[model.alias]?.ttfb_ms ?? -1,
      meta: { className: "text-right" },
      header: ({ column }) => (
        <DataTableColumnHeader
          column={column}
          title="TTFB"
          className="ml-auto"
        />
      ),
      cell: ({ row }) => {
        if (testing === row.original.alias) {
          return (
            <Loader2 className="text-muted-foreground ml-auto size-3.5 animate-spin" />
          );
        }
        const metric = stats[row.original.alias];
        if (!metric) return <Unmeasured />;
        return (
          <span className="font-mono text-xs tabular-nums">
            {formatMs(metric.ttfb_ms)}
          </span>
        );
      },
    },
    {
      id: "p95",
      accessorFn: (model) => stats[model.alias]?.p95_ms ?? -1,
      meta: { className: "text-right" },
      header: ({ column }) => (
        <DataTableColumnHeader
          column={column}
          title="P95"
          className="ml-auto"
        />
      ),
      cell: ({ row }) => {
        const metric = stats[row.original.alias];
        if (!metric) return <Unmeasured />;
        // A withheld tail is a sample-size problem, not a health problem, so
        // it reads as a muted note rather than a failure.
        if (metric.p95_ms === null) {
          return (
            <span
              className="inline-flex items-center gap-1.5"
              title={LOW_SAMPLE_HINT}
            >
              <span className="text-muted-foreground font-mono text-xs">—</span>
              <Badge variant="secondary" className="font-normal">
                Low sample
              </Badge>
            </span>
          );
        }
        return (
          <span
            className={`font-mono text-xs tabular-nums ${latencyTone(metric.p95_ms)}`}
          >
            {formatMs(metric.p95_ms)}
          </span>
        );
      },
    },
    {
      id: "success",
      accessorFn: (model) => stats[model.alias]?.success_rate ?? -1,
      meta: { className: "text-right" },
      header: ({ column }) => (
        <DataTableColumnHeader
          column={column}
          title="Success"
          className="ml-auto"
        />
      ),
      cell: ({ row }) => {
        const metric = stats[row.original.alias];
        if (!metric) return <Unmeasured />;
        return (
          <span
            className={`font-mono text-xs tabular-nums ${successTone(metric.success_rate, metric.confidence)}`}
          >
            {formatRate(metric.success_rate)}
          </span>
        );
      },
    },
    {
      id: "requests",
      accessorFn: (model) => stats[model.alias]?.requests ?? -1,
      meta: { className: "text-right" },
      header: ({ column }) => (
        <DataTableColumnHeader
          column={column}
          title="Req"
          className="ml-auto"
        />
      ),
      cell: ({ row }) => {
        const metric = stats[row.original.alias];
        if (!metric) return <Unmeasured />;
        return (
          <span
            className="font-mono text-xs tabular-nums"
            title={`${formatCount(metric.requests)} request${metric.requests === 1 ? "" : "s"} in the rolling window`}
          >
            {formatCount(metric.requests)}
          </span>
        );
      },
    },
    {
      id: "actions",
      header: "",
      enableSorting: false,
      meta: { className: "text-right" },
      cell: ({ row }) => (
        <DataTableRowActions>
          <DropdownMenuItem
            onSelect={() => {
              setDraft({
                alias: row.original.alias,
                model: row.original.model,
                fallback: row.original.fallback.join(", "),
                capabilities: row.original.capabilities,
              });
              setEditing(true);
            }}
          >
            <Pencil />
            Edit
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={testing !== undefined}
            onSelect={() => runTest(row.original.alias)}
          >
            <Activity />
            Test
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => remove(row.original.alias)}
          >
            <Trash2 />
            Delete
          </DropdownMenuItem>
        </DataTableRowActions>
      ),
    },
  ];

  return (
    <>
      <Link
        href="/providers"
        className="text-muted-foreground hover:text-foreground mb-4 inline-flex items-center gap-1.5 text-sm"
      >
        <ArrowLeft className="size-4" />
        Providers
      </Link>

      <PageHeader
        title={name}
        description="Provider configuration and the model aliases routed to it."
        action={
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => setImporting(true)}>
              <Download className="size-4" />
              Import models
            </Button>
            <Button
              onClick={() => {
                setDraft({
                  alias: provider?.alias_prefix ?? "",
                  model: "",
                  fallback: "",
                  // The conservative baseline every chat upstream serves; the
                  // operator ticks the rest.
                  capabilities: ["chat", "streaming"],
                });
                setEditing(false);
              }}
            >
              Add model
            </Button>
          </div>
        }
      />

      <Card className="mb-6">
        <CardHeader>
          <CardTitle className="text-base">Configuration</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Detail
            label="Group"
            value={
              provider &&
              (PROVIDER_GROUPS.find((g) => g.id === provider.group)?.label ??
                provider.group)
            }
          />
          <Detail label="Wire format" value={provider?.kind} />
          <Detail label="Base URL" value={provider?.base_url} mono />
          <Detail
            label="Timeout"
            value={provider ? `${provider.timeout_ms} ms` : undefined}
          />
          <Detail
            label="Alias prefix"
            value={provider?.alias_prefix || (provider ? "none" : undefined)}
            mono
          />
          <div className="space-y-1">
            <p className="text-muted-foreground text-xs">Status</p>
            {provider ? (
              <div className="flex gap-2">
                {provider.enabled ? (
                  <Badge>enabled</Badge>
                ) : (
                  <Badge variant="outline">disabled</Badge>
                )}
                <Badge variant={provider.has_api_key ? "secondary" : "outline"}>
                  {provider.has_api_key ? "key set" : "no key"}
                </Badge>
              </div>
            ) : (
              <p className="text-muted-foreground text-sm">—</p>
            )}
          </div>
        </CardContent>
      </Card>

      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Kpi
          label="TTFB"
          value={summary && formatMs(summary.ttfb_ms)}
          caption={caption}
          loading={metrics.loading}
        />
        <Kpi
          label="Median"
          value={summary && formatMs(summary.p50_ms)}
          caption={caption}
          loading={metrics.loading}
        />
        <Kpi
          label="P95"
          value={
            summary && summary.p95_ms !== null ? formatMs(summary.p95_ms) : null
          }
          caption={summary && summary.p95_ms === null ? "Low sample" : caption}
          hint={
            summary && summary.p95_ms === null ? LOW_SAMPLE_HINT : undefined
          }
          tone={latencyTone(summary?.p95_ms ?? null)}
          loading={metrics.loading}
        />
        <Kpi
          label="Success"
          value={summary && formatRate(summary.success_rate)}
          caption={caption}
          tone={
            summary ? successTone(summary.success_rate, summary.confidence) : ""
          }
          loading={metrics.loading}
        />
      </div>

      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : (
        <DataTable
          columns={columns}
          data={models}
          empty="No model aliases routed to this provider."
          searchKey="alias"
          searchPlaceholder="Search aliases…"
          enableExport
          exportFilename={`${name}-models`}
          toolbarActions={
            <Button
              variant="outline"
              size="sm"
              disabled={testingAll || models.length === 0}
              onClick={runAllTests}
            >
              {testingAll ? <Loader2 className="animate-spin" /> : <Activity />}
              Test all
            </Button>
          }
        />
      )}

      <ImportModelsDialog
        provider={name}
        aliasPrefix={provider?.alias_prefix ?? ""}
        open={importing}
        onOpenChange={setImporting}
        onImported={reload}
      />

      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editing ? `Edit ${draft?.alias}` : "Add model"}
            </DialogTitle>
            <DialogDescription>
              Fallback aliases are tried in order when an attempt fails with a
              retryable error.
            </DialogDescription>
          </DialogHeader>

          {draft ? (
            <form
              id="model-form"
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                void save();
              }}
            >
              <div className="space-y-2">
                <Label htmlFor="alias">Alias</Label>
                <Input
                  id="alias"
                  value={draft.alias}
                  disabled={editing}
                  required
                  placeholder="gpt-4o"
                  onChange={(e) =>
                    setDraft({ ...draft, alias: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="model">Upstream model</Label>
                <Input
                  id="model"
                  value={draft.model}
                  required
                  placeholder="gpt-4o-mini"
                  onChange={(e) =>
                    setDraft({ ...draft, model: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="fallback">Fallback aliases</Label>
                <Input
                  id="fallback"
                  value={draft.fallback}
                  placeholder="local, cheap"
                  onChange={(e) =>
                    setDraft({ ...draft, fallback: e.target.value })
                  }
                  list="alias-options"
                />
                <datalist id="alias-options">
                  {(data ?? [])
                    .filter((m) => m.alias !== draft.alias)
                    .map((m) => (
                      <option key={m.alias} value={m.alias} />
                    ))}
                </datalist>
                <p className="text-muted-foreground text-xs">
                  Comma-separated aliases, not provider names. Each one is a
                  full route, so a fallback may point at another provider.
                </p>
              </div>
              <div className="space-y-2">
                <Label>Capabilities</Label>
                <div className="grid gap-2 sm:grid-cols-2">
                  {CAPABILITIES.map(({ id, label, description, icon: Icon }) => (
                    <label
                      key={id}
                      title={description}
                      className="hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm"
                    >
                      <input
                        type="checkbox"
                        className="accent-primary size-4"
                        checked={draft.capabilities.includes(id)}
                        onChange={() =>
                          setDraft({
                            ...draft,
                            capabilities: draft.capabilities.includes(id)
                              ? draft.capabilities.filter((c) => c !== id)
                              : [...draft.capabilities, id],
                          })
                        }
                      />
                      <Icon className="text-muted-foreground size-3.5" />
                      {label}
                    </label>
                  ))}
                </div>
                <p className="text-muted-foreground text-xs">
                  Advertised on /v1/models. Imported models are tagged
                  automatically from their name; correct them here when a custom
                  provider differs.
                </p>
              </div>
            </form>
          ) : null}

          <DialogFooter>
            <Button
              variant="outline"
              type="button"
              onClick={() => setDraft(undefined)}
            >
              Cancel
            </Button>
            <Button type="submit" form="model-form" disabled={saving}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function Detail({
  label,
  value,
  mono,
}: {
  label: string;
  value?: string;
  mono?: boolean;
}) {
  return (
    <div className="space-y-1">
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className={mono ? "font-mono text-xs break-all" : "text-sm"}>
        {value ?? "—"}
      </p>
    </div>
  );
}

/** Placeholder for an alias the router has not served since it started. */
function Unmeasured() {
  return (
    <span
      className="text-muted-foreground text-xs"
      title="No requests in window"
    >
      —
    </span>
  );
}

/**
 * Kpi renders one headline metric. A null value means the router has nothing
 * to report — no requests in the window, or a tail it declined to estimate.
 */
function Kpi({
  label,
  value,
  caption,
  loading,
  tone,
  hint,
}: {
  label: string;
  value: string | null | undefined;
  caption?: string;
  loading: boolean;
  tone?: string;
  hint?: string;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
          {label}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-1">
        {loading && value === undefined ? (
          <Skeleton className="h-8 w-20" />
        ) : (
          <span
            className={`font-mono text-2xl font-semibold tabular-nums ${value ? (tone ?? "") : "text-muted-foreground"}`}
            title={hint ?? (value ? undefined : "No requests in window")}
          >
            {value ?? "—"}
          </span>
        )}
        <p className="text-muted-foreground text-xs" title={hint}>
          {caption ?? "\u00a0"}
        </p>
      </CardContent>
    </Card>
  );
}
