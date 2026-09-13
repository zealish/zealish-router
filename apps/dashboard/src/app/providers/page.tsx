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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api, ApiError, PROVIDER_KINDS, type Provider } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Draft = {
  name: string;
  kind: string;
  base_url: string;
  api_key: string;
  timeout_ms: number;
  enabled: boolean;
};

const BLANK: Draft = {
  name: "",
  kind: "openai",
  base_url: "",
  api_key: "",
  timeout_ms: 60000,
  enabled: true,
};

export default function ProvidersPage() {
  const { data, error, reload } = useResource<Provider[]>("/providers");
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  const openEdit = (p: Provider) => {
    setDraft({ ...p, api_key: "" });
    setEditing(true);
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await api.put(`/providers/${encodeURIComponent(draft.name)}`, {
        kind: draft.kind,
        base_url: draft.base_url,
        // An omitted api_key preserves the stored secret server-side.
        ...(draft.api_key ? { api_key: draft.api_key } : {}),
        timeout_ms: Number(draft.timeout_ms),
        enabled: draft.enabled,
      });
      toast.success(`Saved provider '${draft.name}'.`);
      setDraft(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (name: string) => {
    if (!confirm(`Delete provider '${name}'?`)) return;
    try {
      await api.del(`/providers/${encodeURIComponent(name)}`);
      toast.success(`Deleted provider '${name}'.`);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    }
  };

  const columns: ColumnDef<Provider, unknown>[] = [
    { accessorKey: "name", header: "Name" },
    { accessorKey: "kind", header: "Kind" },
    {
      accessorKey: "base_url",
      header: "Base URL",
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.base_url}</span>
      ),
    },
    {
      accessorKey: "has_api_key",
      header: "API key",
      cell: ({ row }) =>
        row.original.has_api_key ? (
          <Badge variant="secondary">set</Badge>
        ) : (
          <Badge variant="outline">none</Badge>
        ),
    },
    {
      accessorKey: "timeout_ms",
      header: "Timeout",
      cell: ({ row }) => `${row.original.timeout_ms} ms`,
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
      id: "actions",
      header: "",
      cell: ({ row }) => (
        <div className="flex justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => openEdit(row.original)}
          >
            Edit
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => remove(row.original.name)}
          >
            Delete
          </Button>
        </div>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title="Providers"
        description="Upstream endpoints the router dispatches to."
        action={
          <Button
            onClick={() => {
              setDraft(BLANK);
              setEditing(false);
            }}
          >
            Add provider
          </Button>
        }
      />

      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : (
        <DataTable
          columns={columns}
          data={data ?? []}
          empty="No providers configured."
        />
      )}

      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editing ? `Edit ${draft?.name}` : "Add provider"}
            </DialogTitle>
            <DialogDescription>
              Stored in the router database and applied without a restart.
            </DialogDescription>
          </DialogHeader>

          {draft ? (
            <form
              id="provider-form"
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                void save();
              }}
            >
              <div className="space-y-2">
                <Label htmlFor="name">Name</Label>
                <Input
                  id="name"
                  value={draft.name}
                  disabled={editing}
                  required
                  onChange={(e) =>
                    setDraft({ ...draft, name: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="kind">Kind</Label>
                <Select
                  value={draft.kind}
                  onValueChange={(kind) => setDraft({ ...draft, kind })}
                >
                  <SelectTrigger id="kind" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PROVIDER_KINDS.map((kind) => (
                      <SelectItem key={kind} value={kind}>
                        {kind}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="base_url">Base URL</Label>
                <Input
                  id="base_url"
                  value={draft.base_url}
                  required
                  placeholder="https://api.openai.com/v1"
                  onChange={(e) =>
                    setDraft({ ...draft, base_url: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="api_key">API key</Label>
                <Input
                  id="api_key"
                  type="password"
                  autoComplete="off"
                  value={draft.api_key}
                  placeholder={
                    editing ? "leave blank to keep the stored key" : "sk-…"
                  }
                  onChange={(e) =>
                    setDraft({ ...draft, api_key: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="timeout_ms">Timeout (ms)</Label>
                <Input
                  id="timeout_ms"
                  type="number"
                  min={1}
                  value={draft.timeout_ms}
                  onChange={(e) =>
                    setDraft({ ...draft, timeout_ms: Number(e.target.value) })
                  }
                />
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id="enabled"
                  checked={draft.enabled}
                  onCheckedChange={(enabled) =>
                    setDraft({ ...draft, enabled })
                  }
                />
                <Label htmlFor="enabled">Enabled</Label>
              </div>
            </form>
          ) : null}

          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setDraft(undefined)}
              type="button"
            >
              Cancel
            </Button>
            <Button type="submit" form="provider-form" disabled={saving}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
