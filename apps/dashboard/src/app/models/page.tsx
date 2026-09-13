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
import { api, ApiError, type ModelAlias, type Provider } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Draft = {
  alias: string;
  provider: string;
  model: string;
  fallback: string;
};

export default function ModelsPage() {
  const { data, error, reload } = useResource<ModelAlias[]>("/models");
  const providers = useResource<Provider[]>("/providers");
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await api.put(`/models/${encodeURIComponent(draft.alias)}`, {
        provider: draft.provider,
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

  const columns: ColumnDef<ModelAlias, unknown>[] = [
    {
      accessorKey: "alias",
      header: "Alias",
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.alias}</span>
      ),
    },
    { accessorKey: "provider", header: "Provider" },
    {
      accessorKey: "model",
      header: "Upstream model",
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.model}</span>
      ),
    },
    {
      accessorKey: "fallback",
      header: "Fallback chain",
      cell: ({ row }) =>
        row.original.fallback.length === 0 ? (
          <span className="text-muted-foreground text-xs">none</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {row.original.fallback.map((name) => (
              <Badge key={name} variant="secondary">
                {name}
              </Badge>
            ))}
          </div>
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
            onClick={() => {
              setDraft({
                ...row.original,
                fallback: row.original.fallback.join(", "),
              });
              setEditing(true);
            }}
          >
            Edit
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => remove(row.original.alias)}
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
        title="Models"
        description="Aliases clients request, mapped onto a provider and upstream model."
        action={
          <Button
            onClick={() => {
              setDraft({
                alias: "",
                provider: providers.data?.[0]?.name ?? "",
                model: "",
                fallback: "",
              });
              setEditing(false);
            }}
          >
            Add alias
          </Button>
        }
      />

      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : (
        <DataTable
          columns={columns}
          data={data ?? []}
          empty="No model aliases configured."
        />
      )}

      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editing ? `Edit ${draft?.alias}` : "Add alias"}
            </DialogTitle>
            <DialogDescription>
              Fallback providers are tried in order when an attempt fails with a
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
                  onChange={(e) => setDraft({ ...draft, alias: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="provider">Provider</Label>
                <Select
                  value={draft.provider}
                  onValueChange={(provider) => setDraft({ ...draft, provider })}
                >
                  <SelectTrigger id="provider" className="w-full">
                    <SelectValue placeholder="Select a provider" />
                  </SelectTrigger>
                  <SelectContent>
                    {(providers.data ?? []).map((p) => (
                      <SelectItem key={p.name} value={p.name}>
                        {p.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="model">Upstream model</Label>
                <Input
                  id="model"
                  value={draft.model}
                  required
                  placeholder="gpt-4o-mini"
                  onChange={(e) => setDraft({ ...draft, model: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="fallback">Fallback providers</Label>
                <Input
                  id="fallback"
                  value={draft.fallback}
                  placeholder="openrouter, ollama"
                  onChange={(e) =>
                    setDraft({ ...draft, fallback: e.target.value })
                  }
                />
                <p className="text-muted-foreground text-xs">
                  Comma-separated provider names.
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
