"use client";

import { useState } from "react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  api,
  ApiError,
  type Extension,
  type SanitizeConfig,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";

export default function ExtensionsPage() {
  const { data, error, loading, reload } = useResource<Extension[]>(
    "/extensions",
  );
  const [busy, setBusy] = useState<string>();

  const update = async (
    ext: Extension,
    enabled: boolean,
    config?: Record<string, unknown>,
  ) => {
    setBusy(ext.id);
    try {
      await api.put<Extension[]>(`/extensions/${encodeURIComponent(ext.id)}`, {
        enabled,
        config: config ?? ext.config,
      });
      await reload();
      toast.success(`${ext.name} ${enabled ? "enabled" : "disabled"}.`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(undefined);
    }
  };

  const saveConfig = async (ext: Extension, config: Record<string, unknown>) => {
    setBusy(ext.id);
    try {
      await api.put<Extension[]>(`/extensions/${encodeURIComponent(ext.id)}`, {
        enabled: ext.enabled,
        config,
      });
      await reload();
      toast.success(`${ext.name} configuration saved.`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(undefined);
    }
  };

  return (
    <>
      <PageHeader
        title="Extensions"
        description="Optional request middleware applied before routing. Changes take effect immediately."
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      {loading ? (
        <div className="space-y-4">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : null}

      <div className="space-y-4">
        {(data ?? []).map((ext) => (
          <Card key={ext.id}>
            <CardHeader className="flex flex-row items-start justify-between gap-4">
              <div>
                <CardTitle className="flex items-center gap-2">
                  {ext.name}
                  <Badge variant={ext.enabled ? "default" : "secondary"}>
                    {ext.enabled ? "Enabled" : "Disabled"}
                  </Badge>
                </CardTitle>
                <p className="text-muted-foreground mt-2 text-sm">
                  {ext.description}
                </p>
              </div>
              <Switch
                checked={ext.enabled}
                disabled={busy === ext.id}
                onCheckedChange={(checked) => void update(ext, checked)}
              />
            </CardHeader>
            {ext.id === "sanitize" ? (
              <CardContent>
                <SanitizeForm
                  key={JSON.stringify(ext.config)}
                  config={ext.config as unknown as SanitizeConfig}
                  disabled={busy === ext.id}
                  onSave={(cfg) =>
                    void saveConfig(ext, cfg as unknown as Record<string, unknown>)
                  }
                />
              </CardContent>
            ) : null}
          </Card>
        ))}
      </div>
    </>
  );
}

function SanitizeForm({
  config,
  disabled,
  onSave,
}: {
  config: SanitizeConfig;
  disabled: boolean;
  onSave: (cfg: SanitizeConfig) => void;
}) {
  const [trim, setTrim] = useState(config.trim_whitespace);
  const [dedup, setDedup] = useState(config.dedup_messages);
  const [window, setWindow] = useState(String(config.history_window ?? 0));

  const dirty =
    trim !== config.trim_whitespace ||
    dedup !== config.dedup_messages ||
    window !== String(config.history_window ?? 0);

  return (
    <div className="border-t pt-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <label className="flex items-center gap-2 text-sm">
          <Switch size="sm" checked={trim} onCheckedChange={setTrim} />
          Trim whitespace
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Switch size="sm" checked={dedup} onCheckedChange={setDedup} />
          Drop duplicate messages
        </label>
        <div className="flex items-center gap-2">
          <Label htmlFor="history-window" className="text-sm whitespace-nowrap">
            History window
          </Label>
          <Input
            id="history-window"
            type="number"
            min={0}
            className="w-24"
            value={window}
            onChange={(e) => setWindow(e.target.value)}
          />
        </div>
      </div>
      <p className="text-muted-foreground mt-2 text-xs">
        History window keeps only the last N non-system messages; 0 disables
        windowing. System prompts are always preserved.
      </p>
      {dirty ? (
        <button
          className="text-primary mt-3 text-sm font-medium underline-offset-4 hover:underline disabled:opacity-50"
          disabled={disabled}
          onClick={() =>
            onSave({
              trim_whitespace: trim,
              dedup_messages: dedup,
              history_window: Math.max(0, Number(window) || 0),
            })
          }
        >
          Save configuration
        </button>
      ) : null}
    </div>
  );
}
