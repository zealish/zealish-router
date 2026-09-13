"use client";

import { useState } from "react";
import { toast } from "sonner";
import type { ColumnDef } from "@tanstack/react-table";
import { DataTable } from "@/components/data-table";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
import { api, ApiError, type ApiKey, type CreatedApiKey } from "@/lib/api";
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
    { accessorKey: "name", header: "Name" },
    {
      accessorKey: "id",
      header: "ID",
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.id}</span>
      ),
    },
    {
      accessorKey: "enabled",
      header: "Status",
      cell: ({ row }) =>
        row.original.enabled ? (
          <Badge>enabled</Badge>
        ) : (
          <Badge variant="outline">disabled</Badge>
        ),
    },
    {
      accessorKey: "created_at",
      header: "Created",
      cell: ({ row }) => new Date(row.original.created_at).toLocaleString(),
    },
    {
      accessorKey: "last_used_at",
      header: "Last used",
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
      cell: ({ row }) => (
        <div className="flex justify-end">
          <Button
            variant="destructive"
            size="sm"
            onClick={() => remove(row.original)}
          >
            Revoke
          </Button>
        </div>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title="API Keys"
        description="Gateway credentials clients send to /v1. Only the hash is stored."
        action={<Button onClick={() => setName("")}>Create key</Button>}
      />

      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : (
        <DataTable columns={columns} data={data ?? []} empty="No API keys." />
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
