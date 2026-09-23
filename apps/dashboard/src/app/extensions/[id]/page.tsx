"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import { toast } from "sonner";
import { Pencil, Plus, Trash2 } from "lucide-react";
import type { ColumnDef } from "@tanstack/react-table";
import {
  DataTable,
  DataTableColumnHeader,
  DataTableRowActions,
} from "@/components/data-table";
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
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import {
  api,
  ApiError,
  type Extension,
  type SystemPromptEntry,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const EXTENSION_TYPES: { value: string; label: string }[] = [
  { value: "system_prompt_injector", label: "System Prompt Injector" },
];

type EntryDraft = {
  name: string;
  prompt: string;
  priority: number;
  enabled: boolean;
};

const blankEntryDraft: EntryDraft = {
  name: "",
  prompt: "",
  priority: 0,
  enabled: true,
};

export default function ExtensionDetailPage() {
  const { id } = useParams<{ id: string }>();
  const {
    data: extension,
    error: extError,
    reload: reloadExt,
  } = useResource<Extension>(`/extensions/${id}`);
  const {
    data: entries,
    error: entriesError,
    reload: reloadEntries,
  } = useResource<SystemPromptEntry[]>(`/extensions/${id}/prompt-entries`);

  const [draft, setDraft] = useState<EntryDraft>();
  const [editing, setEditing] = useState(false);
  const [editEntryId, setEditEntryId] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState<SystemPromptEntry>();

  const openAdd = () => {
    setDraft({ ...blankEntryDraft });
    setEditing(false);
    setEditEntryId(undefined);
  };

  const openEdit = (entry: SystemPromptEntry) => {
    setDraft({
      name: entry.name,
      prompt: entry.prompt,
      priority: entry.priority,
      enabled: entry.enabled,
    });
    setEditing(true);
    setEditEntryId(entry.id);
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const body = {
        name: draft.name,
        prompt: draft.prompt,
        priority: draft.priority,
        enabled: draft.enabled,
      };
      if (editing && editEntryId) {
        await api.put(`/extensions/${id}/prompt-entries/${editEntryId}`, body);
        toast.success(`Updated prompt '${draft.name}'.`);
      } else {
        await api.post(`/extensions/${id}/prompt-entries`, body);
        toast.success(`Created prompt '${draft.name}'.`);
      }
      setDraft(undefined);
      setEditEntryId(undefined);
      await reloadEntries();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (entry: SystemPromptEntry) => {
    try {
      await api.del(`/extensions/${id}/prompt-entries/${entry.id}`);
      toast.success(`Deleted prompt '${entry.name}'.`);
      setDeleting(undefined);
      await reloadEntries();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    }
  };

  const typeLabel =
    EXTENSION_TYPES.find((t) => t.value === extension?.type)?.label ??
    extension?.type ??
    "";

  const columns: ColumnDef<SystemPromptEntry>[] = [
    {
      accessorKey: "name",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Name" />
      ),
    },
    {
      accessorKey: "prompt",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Prompt" />
      ),
      cell: ({ row }) => (
        <span className="text-muted-foreground line-clamp-1 max-w-xs">
          {row.original.prompt.length > 80
            ? row.original.prompt.slice(0, 80) + "…"
            : row.original.prompt}
        </span>
      ),
    },
    {
      accessorKey: "priority",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Priority" />
      ),
    },
    {
      accessorKey: "enabled",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Enabled" />
      ),
      cell: ({ row }) => (
        <Badge variant={row.original.enabled ? "secondary" : "outline"}>
          {row.original.enabled ? "enabled" : "disabled"}
        </Badge>
      ),
    },
    {
      accessorKey: "created_at",
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title="Created" />
      ),
      cell: ({ row }) =>
        new Date(row.original.created_at).toLocaleDateString(),
    },
    {
      id: "actions",
      cell: ({ row }) => (
        <DataTableRowActions>
          <DropdownMenuItem onSelect={() => openEdit(row.original)}>
            <Pencil />
            Edit
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setDeleting(row.original)}
          >
            <Trash2 />
            Delete
          </DropdownMenuItem>
        </DataTableRowActions>
      ),
    },
  ];

  const entryList = entries ?? [];

  return (
    <>
      <PageHeader
        title={extension?.name ?? "Extension"}
        description={typeLabel}
        action={
          <Button onClick={openAdd}>
            <Plus />
            Add Prompt
          </Button>
        }
      />

      {extError ? (
        <p className="text-destructive text-sm">{extError}</p>
      ) : null}
      {entriesError ? (
        <p className="text-destructive text-sm">{entriesError}</p>
      ) : null}

      {extension ? (
        <div className="mb-6 flex flex-wrap gap-1.5">
          <Badge variant="secondary">{typeLabel}</Badge>
          <Badge variant={extension.enabled ? "secondary" : "outline"}>
            {extension.enabled ? "enabled" : "disabled"}
          </Badge>
          <Badge variant="outline">
            Created {new Date(extension.created_at).toLocaleDateString()}
          </Badge>
          <Badge variant="outline">
            Updated {new Date(extension.updated_at).toLocaleDateString()}
          </Badge>
        </div>
      ) : null}

      <DataTable
        columns={columns}
        data={entryList}
        searchKey="name"
        searchPlaceholder="Filter prompts..."
        defaultSorting={[{ id: "priority", desc: true }]}
        empty="No prompt entries yet."
      />

      {/* Delete confirmation */}
      <Dialog
        open={deleting !== undefined}
        onOpenChange={(open) => !open && setDeleting(undefined)}
      >
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Delete prompt entry</DialogTitle>
            <DialogDescription>
              Delete prompt &lsquo;{deleting?.name}&rsquo;? This cannot be
              undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(undefined)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => deleting && remove(deleting)}
            >
              <Trash2 />
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Add / Edit dialog */}
      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editing ? `Edit ${draft?.name}` : "Add prompt entry"}
            </DialogTitle>
            <DialogDescription>
              Configure a system prompt injector entry.
            </DialogDescription>
          </DialogHeader>

          {draft ? (
            <form
              id="prompt-form"
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                void save();
              }}
            >
              <div className="space-y-2">
                <Label htmlFor="prompt-name">Name</Label>
                <Input
                  id="prompt-name"
                  value={draft.name}
                  required
                  onChange={(e) =>
                    setDraft({ ...draft, name: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="prompt-text">Prompt</Label>
                <Textarea
                  id="prompt-text"
                  value={draft.prompt}
                  rows={4}
                  required
                  onChange={(e) =>
                    setDraft({ ...draft, prompt: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="priority">Priority</Label>
                <Input
                  id="priority"
                  type="number"
                  value={draft.priority}
                  onChange={(e) =>
                    setDraft({ ...draft, priority: Number(e.target.value) })
                  }
                />
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id="entry-enabled"
                  checked={draft.enabled}
                  onCheckedChange={(enabled) =>
                    setDraft({ ...draft, enabled })
                  }
                />
                <Label htmlFor="entry-enabled">Enabled</Label>
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
            <Button type="submit" form="prompt-form" disabled={saving}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
