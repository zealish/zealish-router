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
import {
  api,
  ApiError,
  type ModelAlias,
  PROVIDER_GROUPS,
  type ModelTestResult,
  type Provider,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Draft = {
  alias: string;
  model: string;
  fallback: string;
};

export default function ProviderDetailPage() {
  const params = useParams<{ name: string }>();
  const name = decodeURIComponent(params.name);

  const providers = useResource<Provider[]>("/providers");
  const { data, error, reload } = useResource<ModelAlias[]>("/models");
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [importing, setImporting] = useState(false);
  // Latency is a live probe result, not stored state: it only exists for the
  // aliases tested since the page loaded.
  const [tests, setTests] = useState<Record<string, ModelTestResult>>({});
  const [testing, setTesting] = useState<string>();
  const [testingAll, setTestingAll] = useState(false);

  const provider = providers.data?.find((p) => p.name === name);
  const models = (data ?? []).filter((m) => m.provider === name);

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
      setTests((prev) => ({ ...prev, [alias]: result }));
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
    }
  };

  const columns: ColumnDef<ModelAlias, unknown>[] = [
    {
      accessorKey: "alias",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Alias" />
      ),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.alias}</span>
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
      id: "latency",
      accessorFn: (model) => tests[model.alias]?.latency_ms ?? -1,
      meta: { className: "text-right" },
      header: ({ column }) => (
        <DataTableColumnHeader
          column={column}
          title="Latency"
          className="ml-auto"
        />
      ),
      cell: ({ row }) => {
        if (testing === row.original.alias) {
          return (
            <Loader2 className="text-muted-foreground ml-auto size-3.5 animate-spin" />
          );
        }
        const result = tests[row.original.alias];
        if (!result) {
          return <span className="text-muted-foreground text-xs">—</span>;
        }
        return result.ok ? (
          <span className="tabular-nums">{result.latency_ms} ms</span>
        ) : (
          <Badge variant="destructive">failed</Badge>
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
