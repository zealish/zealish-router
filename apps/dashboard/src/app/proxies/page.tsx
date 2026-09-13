"use client";

import { useState } from "react";
import { toast } from "sonner";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  api,
  ApiError,
  type ImportProxiesResult,
  type Provider,
  type Proxy,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Draft = Proxy & { editing: boolean };

const blankDraft = (): Draft => ({
  name: "",
  url: "",
  enabled: true,
  editing: false,
});

export default function ProxiesPage() {
  const { data, error, reload } = useResource<Proxy[]>("/proxies");
  const providers = useResource<Provider[]>("/providers");
  const [draft, setDraft] = useState<Draft>();
  const [saving, setSaving] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const [importEnabled, setImportEnabled] = useState(true);
  const [importOverwrite, setImportOverwrite] = useState(false);
  const [importing, setImporting] = useState(false);

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await api.put(`/proxies/${encodeURIComponent(draft.name.trim())}`, {
        url: draft.url.trim(),
        enabled: draft.enabled,
      });
      toast.success(`Saved proxy '${draft.name}'.`);
      setDraft(undefined);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (name: string) => {
    if (!confirm(`Delete proxy '${name}'?`)) return;
    try {
      await api.del(`/proxies/${encodeURIComponent(name)}`);
      toast.success(`Deleted proxy '${name}'.`);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    }
  };

  const toggle = async (p: Proxy) => {
    try {
      await api.put(`/proxies/${encodeURIComponent(p.name)}`, {
        url: p.url,
        enabled: !p.enabled,
      });
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    }
  };

  const runImport = async () => {
    setImporting(true);
    try {
      const result = await api.post<ImportProxiesResult>("/proxies/import", {
        text: importText,
        enabled: importEnabled,
        overwrite: importOverwrite,
      });
      const parts = [`Imported ${result.imported.length}`];
      if (result.skipped.length > 0) {
        parts.push(`skipped ${result.skipped.length}`);
      }
      if (result.invalid.length > 0) {
        parts.push(`invalid ${result.invalid.length}`);
      }
      toast[result.invalid.length > 0 ? "warning" : "success"](
        `${parts.join(", ")} ${result.imported.length === 1 ? "proxy" : "proxies"}.`,
      );
      if (result.invalid.length > 0) {
        // Leave only the rejected lines in the box so they can be fixed.
        setImportText(result.invalid.join("\n"));
      } else {
        setImportOpen(false);
        setImportText("");
      }
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setImporting(false);
    }
  };

  const proxies = data ?? [];
  const pooledProviders = (providers.data ?? []).filter(
    (p) => p.use_proxy_pool,
  );

  return (
    <>
      <PageHeader
        title="Proxy pool"
        description="Outbound proxies rotated per request for providers that opt into the pool."
        action={
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => setImportOpen(true)}>
              Import
            </Button>
            <Button variant="outline" onClick={() => setDraft(blankDraft())}>
              Add proxy
            </Button>
          </div>
        }
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      {pooledProviders.length > 0 ? (
        <p className="text-muted-foreground mb-4 text-sm">
          Used by:{" "}
          {pooledProviders.map((p) => (
            <Badge key={p.name} variant="secondary" className="mr-1">
              {p.name}
            </Badge>
          ))}
        </p>
      ) : (
        <p className="text-muted-foreground mb-4 text-sm">
          No provider uses the pool yet. Enable &quot;Use proxy pool&quot; on a
          provider to route its traffic through these proxies.
        </p>
      )}

      {proxies.length === 0 ? (
        <p className="text-muted-foreground rounded-xl border border-dashed py-12 text-center text-sm">
          No proxies yet. Add http, https or socks5 endpoints to build the
          pool.
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {proxies.map((p) => (
            <Card key={p.name} className="gap-4">
              <CardHeader>
                <CardTitle className="font-mono">{p.name}</CardTitle>
                <CardDescription className="font-mono text-xs break-all">
                  {p.url}
                </CardDescription>
                <CardAction>
                  {p.enabled ? (
                    <Badge>enabled</Badge>
                  ) : (
                    <Badge variant="outline">disabled</Badge>
                  )}
                </CardAction>
              </CardHeader>
              <CardContent>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setDraft({ ...p, editing: true })}
                  >
                    Edit
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => toggle(p)}>
                    {p.enabled ? "Disable" : "Enable"}
                  </Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    className="ml-auto"
                    onClick={() => remove(p.name)}
                  >
                    Delete
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {draft?.editing ? `Edit ${draft.name}` : "Add proxy"}
            </DialogTitle>
            <DialogDescription>
              Enabled proxies are rotated round-robin across requests from
              providers that use the pool. Applied without a restart.
            </DialogDescription>
          </DialogHeader>

          {draft ? (
            <form
              id="proxy-form"
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
                  disabled={draft.editing}
                  required
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="url">Proxy URL</Label>
                <Input
                  id="url"
                  value={draft.url}
                  required
                  placeholder="http://user:pass@proxy.example.com:8080"
                  className="font-mono text-xs"
                  onChange={(e) => setDraft({ ...draft, url: e.target.value })}
                />
                <p className="text-muted-foreground text-xs">
                  http, https or socks5, with optional credentials in the URL.
                </p>
              </div>
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
              onClick={() => setDraft(undefined)}
              type="button"
            >
              Cancel
            </Button>
            <Button type="submit" form="proxy-form" disabled={saving}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Import proxies</DialogTitle>
            <DialogDescription>
              Paste one proxy per line. Named after host:port; existing names
              are skipped unless overwrite is on.
            </DialogDescription>
          </DialogHeader>

          <form
            id="proxy-import-form"
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              void runImport();
            }}
          >
            <div className="space-y-2">
              <Label htmlFor="import-text">Proxy list</Label>
              <Textarea
                id="import-text"
                value={importText}
                required
                rows={8}
                className="font-mono text-xs"
                placeholder={
                  "http://user:pass@proxy-a:8080\nproxy-b:3128\nproxy-c:1080:user:pass\nsocks5://proxy-d:1080"
                }
                onChange={(e) => setImportText(e.target.value)}
              />
              <p className="text-muted-foreground text-xs">
                Accepts full URLs, host:port, or host:port:user:pass (defaults
                to http). Blank lines and #-comments are ignored.
              </p>
            </div>
            <div className="flex items-center gap-3">
              <Switch
                id="import-enabled"
                checked={importEnabled}
                onCheckedChange={setImportEnabled}
              />
              <Label htmlFor="import-enabled">Enable imported proxies</Label>
            </div>
            <div className="flex items-center gap-3">
              <Switch
                id="import-overwrite"
                checked={importOverwrite}
                onCheckedChange={setImportOverwrite}
              />
              <Label htmlFor="import-overwrite">Overwrite existing</Label>
            </div>
          </form>

          <DialogFooter>
            <Button
              variant="outline"
              type="button"
              onClick={() => setImportOpen(false)}
            >
              Cancel
            </Button>
            <Button type="submit" form="proxy-import-form" disabled={importing}>
              Import
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
