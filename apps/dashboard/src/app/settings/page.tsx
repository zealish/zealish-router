"use client";

import { useState } from "react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError, clearToken, ROUTER_URL, type Settings } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

type Row = { key: string; value: string };

export default function SettingsPage() {
  const { data, error, reload } = useResource<Settings>("/settings");
  const [rows, setRows] = useState<Row[]>([]);
  const [synced, setSynced] = useState<Settings>();
  const [saving, setSaving] = useState(false);

  // Adopt a freshly fetched payload; edits after that stay untouched.
  if (data && data !== synced) {
    setSynced(data);
    setRows(Object.entries(data).map(([key, value]) => ({ key, value })));
  }

  const save = async () => {
    setSaving(true);
    try {
      const payload: Settings = {};
      for (const row of rows) {
        if (row.key.trim()) payload[row.key.trim()] = row.value;
      }
      await api.put<Settings>("/settings", payload);
      toast.success("Settings saved.");
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <PageHeader
        title="Settings"
        description="Key/value settings stored in the router database."
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      <Card>
        <CardHeader>
          <CardTitle>Router settings</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {rows.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No settings stored yet.
            </p>
          ) : null}

          {rows.map((row, index) => (
            <div key={index} className="flex items-end gap-3">
              <div className="flex-1 space-y-2">
                <Label htmlFor={`key-${index}`}>Key</Label>
                <Input
                  id={`key-${index}`}
                  value={row.key}
                  className="font-mono text-xs"
                  onChange={(e) =>
                    setRows(
                      rows.map((r, i) =>
                        i === index ? { ...r, key: e.target.value } : r,
                      ),
                    )
                  }
                />
              </div>
              <div className="flex-1 space-y-2">
                <Label htmlFor={`value-${index}`}>Value</Label>
                <Input
                  id={`value-${index}`}
                  value={row.value}
                  onChange={(e) =>
                    setRows(
                      rows.map((r, i) =>
                        i === index ? { ...r, value: e.target.value } : r,
                      ),
                    )
                  }
                />
              </div>
              <Button
                variant="ghost"
                onClick={() => setRows(rows.filter((_, i) => i !== index))}
              >
                Remove
              </Button>
            </div>
          ))}

          <div className="flex gap-2">
            <Button
              variant="outline"
              onClick={() => setRows([...rows, { key: "", value: "" }])}
            >
              Add setting
            </Button>
            <Button onClick={() => void save()} disabled={saving}>
              Save
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Connection</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-muted-foreground text-sm">
            Router endpoint <span className="font-mono">{ROUTER_URL}</span>, set
            via <span className="font-mono">NEXT_PUBLIC_ROUTER_URL</span>.
          </p>
          <Button variant="outline" onClick={clearToken}>
            Forget admin token
          </Button>
        </CardContent>
      </Card>
    </>
  );
}
