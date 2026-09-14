"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Check, Copy, Gauge, Trash2 } from "lucide-react";
import type { ColumnDef } from "@tanstack/react-table";
import {
  DataTable,
  DataTableColumnHeader,
  DataTableRowActions,
} from "@/components/data-table";
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
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import {
  api,
  ApiError,
  ROUTER_URL,
  type ApiKey,
  type CreatedApiKey,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

export default function KeysPage() {
  const { data, error, reload } = useResource<ApiKey[]>("/keys");
  const [name, setName] = useState<string>();
  const [created, setCreated] = useState<CreatedApiKey>();
  const [saving, setSaving] = useState(false);
  const [quotaKey, setQuotaKey] = useState<ApiKey>();
  const [perMin, setPerMin] = useState("0");
  const [budget, setBudget] = useState("0");

  const create = async () => {
    if (!name) return;
    setSaving(true);
    try {
      setCreated(await api.post<CreatedApiKey>("/keys", { name }));
      setName(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (key: ApiKey) => {
    if (!confirm(`Revoke key '${key.name}'?`)) return;
    try {
      await api.del(`/keys/${encodeURIComponent(key.id)}`);
      toast.success(`Revoked '${key.name}'.`);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    }
  };

  const openQuota = (key: ApiKey) => {
    setPerMin(String(key.rate_limit_per_min));
    setBudget(String(key.monthly_budget_usd));
    setQuotaKey(key);
  };

  const saveQuota = async () => {
    if (!quotaKey) return;
    setSaving(true);
    try {
      await api.put(`/keys/${encodeURIComponent(quotaKey.id)}/quota`, {
        rate_limit_per_min: Number(perMin) || 0,
        monthly_budget_usd: Number(budget) || 0,
      });
      toast.success(`Updated limits for '${quotaKey.name}'.`);
      setQuotaKey(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const columns: ColumnDef<ApiKey, unknown>[] = [
    {
      accessorKey: "name",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Name" />
      ),
    },
    {
      accessorKey: "id",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="ID" />
      ),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.id}</span>
      ),
    },
    {
      id: "enabled",
      accessorFn: (key) => (key.enabled ? "enabled" : "disabled"),
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Status" />
      ),
      cell: ({ row }) =>
        row.original.enabled ? (
          <Badge>enabled</Badge>
        ) : (
          <Badge variant="outline">disabled</Badge>
        ),
    },
    {
      accessorKey: "created_at",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Created" />
      ),
      cell: ({ row }) => new Date(row.original.created_at).toLocaleString(),
    },
    {
      accessorKey: "last_used_at",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Last used" />
      ),
      cell: ({ row }) =>
        row.original.last_used_at ? (
          new Date(row.original.last_used_at).toLocaleString()
        ) : (
          <span className="text-muted-foreground text-xs">never</span>
        ),
    },
    {
      accessorKey: "requests",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Requests" />
      ),
      cell: ({ row }) => (
        <span className="tabular-nums">
          {(row.original.requests ?? 0).toLocaleString()}
        </span>
      ),
    },
    {
      accessorKey: "cost_usd",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Total cost" />
      ),
      cell: ({ row }) => (
        <span className="tabular-nums">{formatCost(row.original.cost_usd)}</span>
      ),
    },
    {
      id: "budget",
      accessorFn: (key) => key.month_spend_usd ?? 0,
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="This month" />
      ),
      cell: ({ row }) => {
        const spent = row.original.month_spend_usd ?? 0;
        const budget = row.original.monthly_budget_usd ?? 0;
        if (!budget) {
          return <span className="tabular-nums">{formatCost(spent)}</span>;
        }
        return (
          <span
            className={`tabular-nums ${spent >= budget ? "text-destructive" : ""}`}
          >
            {formatCost(spent)} / {formatCost(budget)}
          </span>
        );
      },
    },
    {
      accessorKey: "rate_limit_per_min",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Rate limit" />
      ),
      cell: ({ row }) =>
        row.original.rate_limit_per_min ? (
          <span className="tabular-nums">
            {row.original.rate_limit_per_min}/min
          </span>
        ) : (
          <span className="text-muted-foreground text-xs">unlimited</span>
        ),
    },
    {
      id: "actions",
      header: "",
      enableSorting: false,
      meta: { className: "text-right" },
      cell: ({ row }) => (
        <DataTableRowActions>
          <DropdownMenuItem onSelect={() => openQuota(row.original)}>
            <Gauge />
            Edit limits
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => remove(row.original)}
          >
            <Trash2 />
            Revoke
          </DropdownMenuItem>
        </DataTableRowActions>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title="Endpoint & Keys"
        description="Gateway credentials clients send to /v1. Only the hash is stored."
        action={<Button onClick={() => setName("")}>Create key</Button>}
      />

      <EndpointCard />

      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : (
        <DataTable
          columns={columns}
          data={data ?? []}
          empty="No API keys."
          searchKey="name"
          searchPlaceholder="Search keys…"
          enableExport
          exportFilename="api-keys"
        />
      )}

      <Dialog
        open={name !== undefined}
        onOpenChange={(open) => !open && setName(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create API key</DialogTitle>
            <DialogDescription>
              The raw key is shown once and never stored in plaintext.
            </DialogDescription>
          </DialogHeader>
          <form
            id="key-form"
            className="space-y-2"
            onSubmit={(e) => {
              e.preventDefault();
              void create();
            }}
          >
            <Label htmlFor="key-name">Name</Label>
            <Input
              id="key-name"
              value={name ?? ""}
              required
              placeholder="laptop-cli"
              onChange={(e) => setName(e.target.value)}
            />
          </form>
          <DialogFooter>
            <Button
              variant="outline"
              type="button"
              onClick={() => setName(undefined)}
            >
              Cancel
            </Button>
            <Button type="submit" form="key-form" disabled={saving}>
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={created !== undefined}
        onOpenChange={(open) => !open && setCreated(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Key created</DialogTitle>
            <DialogDescription>
              Copy it now — it cannot be retrieved again.
            </DialogDescription>
          </DialogHeader>
          <code className="bg-muted rounded-md px-3 py-2 font-mono text-sm break-all">
            {created?.key}
          </code>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                if (created) {
                  void navigator.clipboard.writeText(created.key);
                  toast.success("Copied to clipboard.");
                }
              }}
            >
              Copy
            </Button>
            <Button onClick={() => setCreated(undefined)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={quotaKey !== undefined}
        onOpenChange={(open) => !open && setQuotaKey(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Limits for {quotaKey?.name}</DialogTitle>
            <DialogDescription>
              Zero means unlimited. The budget resets at the start of each
              calendar month.
            </DialogDescription>
          </DialogHeader>
          <form
            id="quota-form"
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              void saveQuota();
            }}
          >
            <div className="space-y-2">
              <Label htmlFor="quota-rate">Requests per minute</Label>
              <Input
                id="quota-rate"
                type="number"
                min={0}
                step={1}
                value={perMin}
                onChange={(e) => setPerMin(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="quota-budget">Monthly budget (USD)</Label>
              <Input
                id="quota-budget"
                type="number"
                min={0}
                step="0.01"
                value={budget}
                onChange={(e) => setBudget(e.target.value)}
              />
            </div>
          </form>
          <DialogFooter>
            <Button
              variant="outline"
              type="button"
              onClick={() => setQuotaKey(undefined)}
            >
              Cancel
            </Button>
            <Button type="submit" form="quota-form" disabled={saving}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** Sub-cent costs need more precision than a currency formatter gives. */
function formatCost(usd: number | undefined): string {
  if (!usd) return "$0.00";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}

const ENDPOINTS = [
  { label: "Base URL", value: `${ROUTER_URL}/v1` },
  { label: "Chat completions", value: `${ROUTER_URL}/v1/chat/completions` },
  { label: "Models", value: `${ROUTER_URL}/v1/models` },
];

function EndpointCard() {
  const [copied, setCopied] = useState<string>();

  const copy = (value: string) => {
    void navigator.clipboard.writeText(value);
    setCopied(value);
    toast.success("Copied to clipboard.");
    setTimeout(() => setCopied(undefined), 1500);
  };

  return (
    <Card className="mb-6">
      <CardHeader>
        <CardTitle>Endpoint information</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-muted-foreground text-sm">
          OpenAI-compatible API. Send your gateway key as{" "}
          <code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">
            Authorization: Bearer &lt;key&gt;
          </code>
          .
        </p>
        <dl className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {ENDPOINTS.map((item) => (
            <div key={item.label} className="space-y-1">
              <dt className="text-muted-foreground text-xs font-medium">
                {item.label}
              </dt>
              <dd className="flex items-center gap-1">
                <code className="bg-muted flex-1 truncate rounded-md px-2 py-1.5 font-mono text-xs">
                  {item.value}
                </code>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Copy ${item.label}`}
                  onClick={() => copy(item.value)}
                >
                  {copied === item.value ? (
                    <Check className="size-4" />
                  ) : (
                    <Copy className="size-4" />
                  )}
                </Button>
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}
