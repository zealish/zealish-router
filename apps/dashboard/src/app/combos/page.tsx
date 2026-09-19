"use client";

import { useState } from "react";
import { toast } from "sonner";
import {
  ArrowDown,
  ArrowUp,
  ChevronsUpDown,
  Pencil,
  Plus,
  Trash2,
  X,
} from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  api,
  ApiError,
  COMBO_STRATEGIES,
  type Combo,
  type ComboStrategy,
  type ModelAlias,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Draft = Combo & { editing: boolean; originalName?: string };

const blankDraft = (): Draft => ({
  name: "",
  strategy: "fallback",
  members: [],
  weights: [],
  enabled: true,
  editing: false,
});

/** One weight per member, defaulting to an equal share. */
const weightAt = (weights: number[], index: number) => weights[index] ?? 1;

export default function CombosPage() {
  const { data, error, reload } = useResource<Combo[]>("/combos");
  const models = useResource<ModelAlias[]>("/models");
  const [draft, setDraft] = useState<Draft>();
  const [saving, setSaving] = useState(false);
  const [picking, setPicking] = useState(false);
  const [deleting, setDeleting] = useState<string>();
  const [removing, setRemoving] = useState(false);

  const save = async () => {
    if (!draft) return;
    const name = draft.name.trim();
    if (!name) {
      toast.error("Enter a combo name.");
      return;
    }
    if (draft.members.length === 0) {
      toast.error("Add at least one model to the pool.");
      return;
    }
    setSaving(true);
    try {
      const oldName = draft.originalName?.trim();
      if (draft.editing && oldName && oldName !== name) {
        await api.post(`/combos/${encodeURIComponent(oldName)}/rename`, {
          name,
        });
        setDraft({ ...draft, name, originalName: name });
      }
      await api.put(`/combos/${encodeURIComponent(name)}`, {
        strategy: draft.strategy,
        members: draft.members,
        weights:
          draft.strategy === "weighted"
            ? draft.members.map((_, i) => weightAt(draft.weights, i))
            : [],
        enabled: draft.enabled,
      });
      toast.success(
        draft.editing && oldName && oldName !== name
          ? `Renamed and saved combo '${name}'.`
          : `Saved combo '${name}'.`,
      );
      setDraft(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (name: string) => {
    setRemoving(true);
    try {
      await api.del(`/combos/${encodeURIComponent(name)}`);
      toast.success(`Deleted combo '${name}'.`);
      setDeleting(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setRemoving(false);
    }
  };

  const combos = data ?? [];
  const aliases = models.data ?? [];
  const available = aliases.filter((m) => !draft?.members.includes(m.alias));

  const move = (index: number, by: number) => {
    if (!draft) return;
    const target = index + by;
    if (target < 0 || target >= draft.members.length) return;
    const members = [...draft.members];
    const weights = draft.members.map((_, i) => weightAt(draft.weights, i));
    [members[index], members[target]] = [members[target], members[index]];
    [weights[index], weights[target]] = [weights[target], weights[index]];
    setDraft({ ...draft, members, weights });
  };

  const removeMember = (index: number) => {
    if (!draft) return;
    setDraft({
      ...draft,
      members: draft.members.filter((_, i) => i !== index),
      weights: draft.members
        .map((_, i) => weightAt(draft.weights, i))
        .filter((_, i) => i !== index),
    });
  };

  const setWeight = (index: number, weight: number) => {
    if (!draft) return;
    const weights = draft.members.map((_, i) => weightAt(draft.weights, i));
    weights[index] = weight;
    setDraft({ ...draft, weights });
  };

  return (
    <>
      <PageHeader
        title="Combos"
        description="Virtual models: one name backed by a pool of aliases, tried in strategy order."
        action={
          <Button variant="outline" onClick={() => setDraft(blankDraft())}>
            <Plus />
            Create combo
          </Button>
        }
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      {combos.length === 0 ? (
        <p className="text-muted-foreground rounded-xl border border-dashed py-12 text-center text-sm">
          No combos yet. Create one to address several models under a single
          name.
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {combos.map((c) => (
            <Card key={c.name} className="gap-4">
              <CardHeader>
                <CardTitle className="font-mono">{c.name}</CardTitle>
                <CardDescription>
                  {COMBO_STRATEGIES.find((s) => s.id === c.strategy)?.label ??
                    c.strategy}
                </CardDescription>
                <CardAction>
                  {c.enabled ? (
                    <Badge>enabled</Badge>
                  ) : (
                    <Badge variant="outline">disabled</Badge>
                  )}
                </CardAction>
              </CardHeader>
              <CardContent className="space-y-4">
                <ol className="space-y-1 text-sm">
                  {c.members.map((member, i) => (
                    <li key={member} className="flex items-center gap-2">
                      <span className="text-muted-foreground w-4 text-xs">
                        {i + 1}
                      </span>
                      <span className="font-mono break-all">{member}</span>
                      {c.strategy === "weighted" ? (
                        <span className="text-muted-foreground ml-auto text-xs">
                          ×{weightAt(c.weights, i)}
                        </span>
                      ) : null}
                    </li>
                  ))}
                </ol>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setDraft({ ...c, editing: true, originalName: c.name })
                    }
                  >
                    <Pencil />
                    Edit
                  </Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    className="ml-auto"
                    onClick={() => setDeleting(c.name)}
                  >
                    <Trash2 />
                    Delete
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog
        open={deleting !== undefined}
        onOpenChange={(open) => !open && setDeleting(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete combo</DialogTitle>
            <DialogDescription>
              Delete combo{" "}
              <span className="text-foreground font-mono">{deleting}</span>?
              Clients requesting this name will no longer resolve. This cannot
              be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              type="button"
              onClick={() => setDeleting(undefined)}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={removing}
              onClick={() => deleting && void remove(deleting)}
            >
              <Trash2 />
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {draft?.editing ? `Edit ${draft.name}` : "Create combo"}
            </DialogTitle>
            <DialogDescription>
              Clients request the combo name as a model; the router walks its
              pool, falling back on rate limits and upstream failures.
            </DialogDescription>
          </DialogHeader>

          {draft ? (
            <form
              id="combo-form"
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
                  required
                  value={draft.name}
                  placeholder="code-agent"
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="strategy">Strategy</Label>
                <Select
                  value={draft.strategy}
                  onValueChange={(strategy) =>
                    setDraft({
                      ...draft,
                      strategy: strategy as ComboStrategy,
                    })
                  }
                >
                  <SelectTrigger id="strategy" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {COMBO_STRATEGIES.map((s) => (
                      <SelectItem key={s.id} value={s.id}>
                        {s.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  {
                    COMBO_STRATEGIES.find((s) => s.id === draft.strategy)
                      ?.description
                  }
                </p>
              </div>

              <div className="space-y-2">
                <Label>Pool</Label>
                {draft.members.length === 0 ? (
                  <p className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs">
                    No models in the pool yet.
                  </p>
                ) : (
                  <ol className="space-y-1">
                    {draft.members.map((member, i) => (
                      <li
                        key={member}
                        className="flex items-center gap-2 rounded-md border px-2 py-1"
                      >
                        <span className="text-muted-foreground w-4 text-xs">
                          {i + 1}
                        </span>
                        <span className="flex-1 font-mono text-xs break-all">
                          {member}
                        </span>
                        {draft.strategy === "weighted" ? (
                          <Input
                            type="number"
                            min={1}
                            className="h-8 w-16"
                            aria-label={`Weight for ${member}`}
                            value={weightAt(draft.weights, i)}
                            onChange={(e) =>
                              setWeight(i, Math.max(1, Number(e.target.value)))
                            }
                          />
                        ) : null}
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          onClick={() => move(i, -1)}
                        >
                          <ArrowUp />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          onClick={() => move(i, 1)}
                        >
                          <ArrowDown />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          onClick={() => removeMember(i)}
                        >
                          <X />
                        </Button>
                      </li>
                    ))}
                  </ol>
                )}
              </div>

              {available.length > 0 ? (
                <div className="space-y-2">
                  <Label htmlFor="add-member">Add model</Label>
                  <Popover open={picking} onOpenChange={setPicking}>
                    <PopoverTrigger asChild>
                      <Button
                        id="add-member"
                        type="button"
                        variant="outline"
                        role="combobox"
                        aria-expanded={picking}
                        className="text-muted-foreground w-full justify-between font-normal"
                      >
                        Pick a model alias
                        <ChevronsUpDown className="opacity-50" />
                      </Button>
                    </PopoverTrigger>
                    <PopoverContent className="w-(--radix-popover-trigger-width) p-0">
                      <Command>
                        <CommandInput placeholder="Search models…" />
                        <CommandList>
                          <CommandEmpty>No model found.</CommandEmpty>
                          <CommandGroup>
                            {available.map((m) => (
                              <CommandItem
                                key={m.alias}
                                value={m.alias}
                                onSelect={() => {
                                  setDraft({
                                    ...draft,
                                    members: [...draft.members, m.alias],
                                    weights: [
                                      ...draft.members.map((_, i) =>
                                        weightAt(draft.weights, i),
                                      ),
                                      1,
                                    ],
                                  });
                                  setPicking(false);
                                }}
                              >
                                <span className="font-mono text-xs break-all">
                                  {m.alias}
                                </span>
                                <span className="text-muted-foreground ml-auto text-xs">
                                  {m.provider}
                                </span>
                              </CommandItem>
                            ))}
                          </CommandGroup>
                        </CommandList>
                      </Command>
                    </PopoverContent>
                  </Popover>
                </div>
              ) : null}

              <div className="flex items-center gap-3">
                <Switch
                  id="enabled"
                  checked={draft.enabled}
                  onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
                />
                <Label htmlFor="enabled">Enabled</Label>
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
            <Button type="submit" form="combo-form" disabled={saving}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
