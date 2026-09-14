"use client";

import Link from "next/link";
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
  PROVIDER_GROUPS,
  PROVIDER_KINDS,
  type CatalogEntry,
  type ModelAlias,
  type Provider,
  type ProviderGroup,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Draft = {
  name: string;
  group: ProviderGroup;
  catalog_id: string;
  kind: string;
  base_url: string;
  api_key: string;
  timeout_ms: number;
  enabled: boolean;
  alias_prefix: string;
  use_proxy_pool: boolean;
  // Empty string means "inherit the global policy"; the API wants null.
  breaker_threshold: string;
  breaker_cooldown_ms: string;
};

const blankDraft = (group: ProviderGroup): Draft => ({
  name: "",
  group,
  catalog_id: "",
  kind: group === "custom" ? "openai" : "anthropic",
  base_url: "",
  api_key: "",
  timeout_ms: 60000,
  enabled: true,
  alias_prefix: "",
  use_proxy_pool: false,
  breaker_threshold: "",
  breaker_cooldown_ms: "",
});


/** Default namespace suggested for a provider's imported aliases. */
const prefixFor = (name: string) => (name ? `${name.trim()}/` : "");

const groupOf = (p: Provider): ProviderGroup => p.group ?? "custom";

/**
 * CircuitBadge surfaces the router's breaker state. A healthy provider shows
 * nothing: the badge is an exception report, not a status line.
 */
function CircuitBadge({ provider }: { provider: Provider }) {
  const state = provider.circuit ?? "closed";
  if (state === "closed") return null;

  const retryAt = provider.circuit_retry_at
    ? new Date(provider.circuit_retry_at).toLocaleTimeString()
    : undefined;

  return state === "open" ? (
    <Badge
      variant="destructive"
      title={retryAt ? `Next probe at ${retryAt}` : undefined}
    >
      circuit open
    </Badge>
  ) : (
    <Badge variant="outline" title="Probing whether the provider recovered">
      probing
    </Badge>
  );
}

export default function ProvidersPage() {
  const { data, error, reload } = useResource<Provider[]>("/providers");
  const models = useResource<ModelAlias[]>("/models");
  const catalog = useResource<CatalogEntry[]>("/provider-catalog");
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  const openAdd = (group: ProviderGroup) => {
    setDraft(blankDraft(group));
    setEditing(false);
  };

  const openEdit = (p: Provider) => {
    setDraft({
      ...p,
      group: groupOf(p),
      catalog_id: p.catalog_id ?? "",
      api_key: "",
      // Null means inherited, which the form shows as an empty field.
      breaker_threshold: p.breaker_threshold?.toString() ?? "",
      breaker_cooldown_ms: p.breaker_cooldown_ms?.toString() ?? "",
    });
    setEditing(true);
  };

  // Picking a preset fills in the endpoint, dialect and namespace, so only the
  // credential is left to type.
  const applyPreset = (entry: CatalogEntry) => {
    if (!draft) return;
    setDraft({
      ...draft,
      catalog_id: entry.id,
      kind: entry.kind,
      base_url: entry.base_url,
      name: draft.name || entry.id,
      alias_prefix: draft.alias_prefix || entry.alias_prefix,
    });
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await api.put(`/providers/${encodeURIComponent(draft.name)}`, {
        group: draft.group,
        catalog_id: draft.catalog_id,
        kind: draft.kind,
        base_url: draft.base_url,
        // An omitted api_key preserves the stored secret server-side.
        ...(draft.api_key ? { api_key: draft.api_key } : {}),
        timeout_ms: Number(draft.timeout_ms),
        enabled: draft.enabled,
        alias_prefix: draft.alias_prefix.trim(),
        use_proxy_pool: draft.use_proxy_pool,
        // A blank field clears the override; null reads as "inherit".
        breaker_threshold:
          draft.breaker_threshold.trim() === ""
            ? null
            : Number(draft.breaker_threshold),
        breaker_cooldown_ms:
          draft.breaker_cooldown_ms.trim() === ""
            ? null
            : Number(draft.breaker_cooldown_ms),
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

  const providers = data ?? [];
  const groupSpec = PROVIDER_GROUPS.find((g) => g.id === draft?.group);
  const presets = (catalog.data ?? []).filter((e) => e.group === draft?.group);

  return (
    <>
      <PageHeader
        title="Providers"
        description="Upstream endpoints the router dispatches to, by how they authenticate."
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      <div className="space-y-8">
        {PROVIDER_GROUPS.map((group) => {
          const members = providers.filter((p) => groupOf(p) === group.id);
          return (
            <section key={group.id} className="space-y-4">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <h2 className="text-lg font-semibold">{group.label}</h2>
                  <p className="text-muted-foreground text-sm">
                    {group.description}
                  </p>
                </div>
                <Button variant="outline" onClick={() => openAdd(group.id)}>
                  Add
                </Button>
              </div>

              {members.length === 0 ? (
                <p className="text-muted-foreground rounded-xl border border-dashed py-8 text-center text-sm">
                  No {group.label.toLowerCase()}s configured.
                </p>
              ) : (
                <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                  {members.map((p) => {
                    const count = (models.data ?? []).filter(
                      (m) => m.provider === p.name,
                    ).length;
                    return (
                      <Card key={p.name} className="gap-4">
                        <CardHeader>
                          <CardTitle>
                            <Link
                              href={`/providers/${encodeURIComponent(p.name)}`}
                              className="hover:underline"
                            >
                              {p.name}
                            </Link>
                          </CardTitle>
                          <CardDescription className="font-mono text-xs break-all">
                            {p.base_url}
                          </CardDescription>
                          <CardAction className="flex items-center gap-1.5">
                            <CircuitBadge provider={p} />
                            {p.enabled ? (
                              <Badge>enabled</Badge>
                            ) : (
                              <Badge variant="outline">disabled</Badge>
                            )}
                          </CardAction>
                        </CardHeader>
                        <CardContent className="space-y-4">
                          <div className="flex flex-wrap gap-1.5">
                            <Badge variant="secondary">{p.kind}</Badge>
                            {p.catalog_id ? (
                              <Badge variant="outline">{p.catalog_id}</Badge>
                            ) : null}
                            <Badge variant="outline">
                              {count} {count === 1 ? "model" : "models"}
                            </Badge>
                            <Badge variant="outline">{p.timeout_ms} ms</Badge>
                            <Badge
                              variant={p.has_api_key ? "secondary" : "outline"}
                            >
                              {p.has_api_key
                                ? group.id === "oauth"
                                  ? "token set"
                                  : "key set"
                                : "no credential"}
                            </Badge>
                            {p.alias_prefix ? (
                              <Badge variant="outline" className="font-mono">
                                {p.alias_prefix}
                              </Badge>
                            ) : null}
                            {p.use_proxy_pool ? (
                              <Badge variant="outline">proxy pool</Badge>
                            ) : null}
                          </div>
                          <div className="flex gap-2">
                            <Button variant="outline" size="sm" asChild>
                              <Link
                                href={`/providers/${encodeURIComponent(p.name)}`}
                              >
                                Models
                              </Link>
                            </Button>
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => openEdit(p)}
                            >
                              Edit
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
                    );
                  })}
                </div>
              )}
            </section>
          );
        })}
      </div>

      <Dialog
        open={draft !== undefined}
        onOpenChange={(open) => !open && setDraft(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editing
                ? `Edit ${draft?.name}`
                : `Add ${groupSpec?.label.toLowerCase() ?? "provider"}`}
            </DialogTitle>
            <DialogDescription>
              {groupSpec?.description} Stored in the router database and applied
              without a restart.
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
              {draft.group !== "custom" && presets.length > 0 ? (
                <div className="space-y-2">
                  <Label htmlFor="preset">Preset</Label>
                  <Select
                    value={draft.catalog_id}
                    onValueChange={(id) => {
                      const entry = presets.find((e) => e.id === id);
                      if (entry) applyPreset(entry);
                    }}
                  >
                    <SelectTrigger id="preset" className="w-full">
                      <SelectValue placeholder="Pick a known upstream" />
                    </SelectTrigger>
                    <SelectContent>
                      {presets.map((entry) => (
                        <SelectItem key={entry.id} value={entry.id}>
                          {entry.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-xs">
                    Fills in the endpoint and wire format; you only supply the
                    credential.
                  </p>
                </div>
              ) : null}
              <div className="space-y-2">
                <Label htmlFor="name">Name</Label>
                <Input
                  id="name"
                  value={draft.name}
                  disabled={editing}
                  required
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      name: e.target.value,
                      // Track the name until the prefix is edited by hand, so
                      // new providers get a namespace without extra typing.
                      alias_prefix:
                        draft.alias_prefix === prefixFor(draft.name)
                          ? prefixFor(e.target.value)
                          : draft.alias_prefix,
                    })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="alias_prefix">Alias prefix</Label>
                <Input
                  id="alias_prefix"
                  value={draft.alias_prefix}
                  placeholder={prefixFor(draft.name) || "acme/"}
                  className="font-mono text-xs"
                  onChange={(e) =>
                    setDraft({ ...draft, alias_prefix: e.target.value })
                  }
                />
                <p className="text-muted-foreground text-xs">
                  Prepended to imported model names, so two providers offering
                  the same model do not collide. Leave blank to import
                  unprefixed.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="kind">Wire format</Label>
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
                        {kind}-compatible
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
                <Label htmlFor="api_key">
                  {draft.group === "oauth" ? "OAuth access token" : "API key"}
                </Label>
                <Input
                  id="api_key"
                  type="password"
                  autoComplete="off"
                  required={!editing && draft.group !== "custom"}
                  value={draft.api_key}
                  placeholder={
                    editing
                      ? "leave blank to keep the stored credential"
                      : draft.group === "oauth"
                        ? "access token from the provider's OAuth flow"
                        : "sk-…"
                  }
                  onChange={(e) =>
                    setDraft({ ...draft, api_key: e.target.value })
                  }
                />
                {draft.group === "oauth" ? (
                  <p className="text-muted-foreground text-xs">
                    Obtained out of band and sent as a bearer token. Replace it
                    here when it expires.
                  </p>
                ) : null}
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
                  id="use_proxy_pool"
                  checked={draft.use_proxy_pool}
                  onCheckedChange={(use_proxy_pool) =>
                    setDraft({ ...draft, use_proxy_pool })
                  }
                />
                <Label htmlFor="use_proxy_pool">Use proxy pool</Label>
              </div>
              <div className="flex items-center gap-3">
                <Switch
                  id="enabled"
                  checked={draft.enabled}
                  onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
                />
                <Label htmlFor="enabled">Enabled</Label>
              </div>
              <div className="space-y-2 border-t pt-4">
                <Label className="text-muted-foreground text-xs font-normal">
                  Circuit breaker — leave blank to inherit the defaults from
                  config.yaml
                </Label>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-2">
                    <Label htmlFor="breaker_threshold">
                      Failure threshold
                    </Label>
                    <Input
                      id="breaker_threshold"
                      type="number"
                      min={0}
                      placeholder="inherited"
                      value={draft.breaker_threshold}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          breaker_threshold: e.target.value,
                        })
                      }
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="breaker_cooldown_ms">Cooldown (ms)</Label>
                    <Input
                      id="breaker_cooldown_ms"
                      type="number"
                      min={0}
                      placeholder="inherited"
                      value={draft.breaker_cooldown_ms}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          breaker_cooldown_ms: e.target.value,
                        })
                      }
                    />
                  </div>
                </div>
                <p className="text-muted-foreground text-xs">
                  0 disables the breaker for this provider.
                </p>
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
