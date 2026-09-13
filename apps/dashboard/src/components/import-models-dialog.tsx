"use client";

import { useEffect, useState } from "react";
import { Loader2, Search } from "lucide-react";
import { toast } from "sonner";
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
import {
  api,
  ApiError,
  type CatalogModel,
  type ImportModelsResult,
} from "@/lib/api";

/**
 * Fetches {base_url}/models from an upstream provider and imports the selected
 * entries as model aliases routed to that provider.
 */
export function ImportModelsDialog({
  provider,
  aliasPrefix,
  open,
  onOpenChange,
  onImported,
}: {
  provider: string;
  aliasPrefix: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onImported: () => Promise<void> | void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open ? (
        <ImportModelsBody
          provider={provider}
          aliasPrefix={aliasPrefix}
          onOpenChange={onOpenChange}
          onImported={onImported}
        />
      ) : null}
    </Dialog>
  );
}

// Mounted only while the dialog is open, so state starts fresh on every open —
// no imperative resets needed inside the fetch effect.
function ImportModelsBody({
  provider,
  aliasPrefix,
  onOpenChange,
  onImported,
}: {
  provider: string;
  aliasPrefix: string;
  onOpenChange: (open: boolean) => void;
  onImported: () => Promise<void> | void;
}) {
  const [catalog, setCatalog] = useState<CatalogModel[]>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [importing, setImporting] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState("");
  const [prefix, setPrefix] = useState(aliasPrefix);
  const [overwrite, setOverwrite] = useState(false);

  useEffect(() => {
    let cancelled = false;

    api
      .get<CatalogModel[]>(`/providers/${encodeURIComponent(provider)}/catalog`)
      .then((models) => {
        if (!cancelled) setCatalog(models);
      })
      .catch((err: unknown) => {
        if (!cancelled)
          setError(err instanceof ApiError ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [provider]);

  const needle = filter.trim().toLowerCase();
  const visible = (catalog ?? []).filter((m) =>
    needle ? m.id.toLowerCase().includes(needle) : true,
  );
  const selectable = visible.filter((m) => overwrite || !m.imported);
  const allSelected =
    selectable.length > 0 && selectable.every((m) => selected.has(m.id));

  const toggle = (id: string) => {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setSelected(next);
  };

  const toggleAll = () => {
    if (allSelected) {
      setSelected(new Set());
      return;
    }
    setSelected(new Set(selectable.map((m) => m.id)));
  };

  const runImport = async () => {
    if (selected.size === 0) return;
    setImporting(true);
    try {
      const result = await api.post<ImportModelsResult>(
        `/providers/${encodeURIComponent(provider)}/import`,
        { models: [...selected], prefix, overwrite },
      );
      const skipped = result.skipped.length
        ? `, ${result.skipped.length} skipped`
        : "";
      toast.success(`Imported ${result.imported.length} model(s)${skipped}.`);
      await onImported();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setImporting(false);
    }
  };

  return (
    <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Import models</DialogTitle>
          <DialogDescription>
            Models advertised by {provider} at its /models endpoint. Each
            selected entry becomes an alias routed to this provider.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="import-prefix">Alias prefix</Label>
            <Input
              id="import-prefix"
              value={prefix}
              placeholder={`${provider}/`}
              className="font-mono text-xs"
              onChange={(e) => setPrefix(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              Defaults to the provider&apos;s configured prefix.
            </p>
          </div>
          <div className="flex items-end gap-3 pb-1">
            <Switch
              id="import-overwrite"
              checked={overwrite}
              onCheckedChange={setOverwrite}
            />
            <Label htmlFor="import-overwrite">Overwrite existing aliases</Label>
          </div>
        </div>

        <div className="relative">
          <Search className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2" />
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter models…"
            className="pl-9"
          />
        </div>

        <div className="max-h-80 overflow-y-auto rounded-lg border">
          {loading ? (
            <p className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
              <Loader2 className="size-4 animate-spin" />
              Fetching catalogue…
            </p>
          ) : error ? (
            <p className="text-destructive px-4 py-10 text-center text-sm">
              {error}
            </p>
          ) : visible.length === 0 ? (
            <p className="text-muted-foreground py-10 text-center text-sm">
              No models returned by this provider.
            </p>
          ) : (
            <ul className="divide-y">
              {visible.map((m) => {
                const locked = m.imported && !overwrite;
                return (
                  <li key={m.id}>
                    <label
                      className={
                        "flex items-center gap-3 px-4 py-2.5 text-sm " +
                        (locked
                          ? "text-muted-foreground"
                          : "hover:bg-muted/50 cursor-pointer")
                      }
                    >
                      <input
                        type="checkbox"
                        className="accent-primary size-4"
                        disabled={locked}
                        checked={selected.has(m.id)}
                        onChange={() => toggle(m.id)}
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-xs">
                        {m.id}
                      </span>
                      <span className="text-muted-foreground hidden min-w-0 flex-1 truncate font-mono text-xs sm:inline">
                        → {prefix + m.id}
                      </span>
                      {m.imported ? (
                        <Badge variant="secondary">{m.alias}</Badge>
                      ) : null}
                    </label>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        <DialogFooter className="sm:justify-between">
          <Button
            variant="outline"
            type="button"
            disabled={selectable.length === 0}
            onClick={toggleAll}
          >
            {allSelected ? "Clear selection" : `Select all (${selectable.length})`}
          </Button>
          <div className="flex gap-2">
            <Button
              variant="outline"
              type="button"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              disabled={importing || selected.size === 0}
              onClick={() => void runImport()}
            >
              Import {selected.size > 0 ? `(${selected.size})` : ""}
            </Button>
          </div>
        </DialogFooter>
    </DialogContent>
  );
}
