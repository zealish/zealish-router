"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Check, Copy, Trash2 } from "lucide-react";
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
      id: "actions",
      header: "",
      enableSorting: false,
      meta: { className: "text-right" },
      cell: ({ row }) => (
        <DataTableRowActions>
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
    </>
  );
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
