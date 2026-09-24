"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { BarChart3, ExternalLink } from "lucide-react";
import Link from "next/link";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import { api, ApiError, type Settings } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

export default function PonytailSettingsPage() {
  const { data, error, reload } = useResource<Settings>("/ponytail/settings");
  const [saving, setSaving] = useState(false);

  const [enabled, setEnabled] = useState(false);
  const [mode, setMode] = useState("balanced");
  const [metadata, setMetadata] = useState(false);
  const [minInputTokens, setMinInputTokens] = useState("12000");
  const [minMessages, setMinMessages] = useState("16");
  const [protectedWindow, setProtectedWindow] = useState("8");
  const [compressionConversation, setCompressionConversation] = useState(true);
  const [compressionCode, setCompressionCode] = useState(true);
  const [compressionDeduplicate, setCompressionDeduplicate] = useState(true);

  // Sync fetched data into local state once per payload.
  useEffect(() => {
    if (!data) return;
    setEnabled(data["ponytail.enabled"] === "true");
    setMode(data["ponytail.mode"] ?? "balanced");
    setMetadata(data["ponytail.metadata"] === "true");
    setMinInputTokens(data["ponytail.thresholds.min_input_tokens"] ?? "12000");
    setMinMessages(data["ponytail.thresholds.min_messages"] ?? "16");
    setProtectedWindow(data["ponytail.protected_window"] ?? "8");
    setCompressionConversation(
      data["ponytail.compression.conversation"] === "true",
    );
    setCompressionCode(data["ponytail.compression.code"] === "true");
    setCompressionDeduplicate(
      data["ponytail.compression.deduplicate"] === "true",
    );
  }, [data]);

  const save = async () => {
    setSaving(true);
    try {
      const payload: Settings = {
        "ponytail.enabled": String(enabled),
        "ponytail.mode": mode,
        "ponytail.metadata": String(metadata),
        "ponytail.thresholds.min_input_tokens": minInputTokens,
        "ponytail.thresholds.min_messages": minMessages,
        "ponytail.protected_window": protectedWindow,
        "ponytail.compression.conversation": String(compressionConversation),
        "ponytail.compression.code": String(compressionCode),
        "ponytail.compression.deduplicate": String(compressionDeduplicate),
      };
      await api.put<Settings>("/ponytail/settings", payload);
      toast.success("Settings saved.");
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    setEnabled(false);
    setMode("balanced");
    setMetadata(false);
    setMinInputTokens("12000");
    setMinMessages("16");
    setProtectedWindow("8");
    setCompressionConversation(true);
    setCompressionCode(true);
    setCompressionDeduplicate(true);
    toast.info("Reset to defaults. Save to apply.");
  };

  return (
    <>
      <PageHeader
        title="Ponytail"
        description="Context optimization settings."
        action={
          <Button variant="outline" size="sm" asChild>
            <Link href="/ponytail/analytics">
              <BarChart3 className="mr-1.5 size-3.5" />
              Analytics
              <ExternalLink className="ml-1 size-3" />
            </Link>
          </Button>
        }
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      <Card>
        <CardHeader>
          <CardTitle>General</CardTitle>
        </CardHeader>
        <CardContent className="space-y-6">
          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Enabled</Label>
              <p className="text-muted-foreground text-xs">
                Activate Ponytail context optimization.
              </p>
            </div>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </div>

          <div className="space-y-2">
            <Label>Mode</Label>
            <Select value={mode} onValueChange={setMode}>
              <SelectTrigger className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="conservative">Conservative</SelectItem>
                <SelectItem value="balanced">Balanced</SelectItem>
                <SelectItem value="aggressive">Aggressive</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Metadata</Label>
              <p className="text-muted-foreground text-xs">
                Include optimization metadata in responses.
              </p>
            </div>
            <Switch checked={metadata} onCheckedChange={setMetadata} />
          </div>
        </CardContent>
      </Card>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Thresholds</CardTitle>
        </CardHeader>
        <CardContent className="space-y-6">
          <div className="space-y-2">
            <Label htmlFor="min-input-tokens">Min input tokens</Label>
            <Input
              id="min-input-tokens"
              type="number"
              min={0}
              value={minInputTokens}
              onChange={(e) => setMinInputTokens(e.target.value)}
              className="w-48 font-mono"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="min-messages">Min messages</Label>
            <Input
              id="min-messages"
              type="number"
              min={0}
              value={minMessages}
              onChange={(e) => setMinMessages(e.target.value)}
              className="w-48 font-mono"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="protected-window">Protected window</Label>
            <Input
              id="protected-window"
              type="number"
              min={0}
              value={protectedWindow}
              onChange={(e) => setProtectedWindow(e.target.value)}
              className="w-48 font-mono"
            />
          </div>
        </CardContent>
      </Card>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Compression</CardTitle>
        </CardHeader>
        <CardContent className="space-y-6">
          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Conversation</Label>
              <p className="text-muted-foreground text-xs">
                Compress conversation messages.
              </p>
            </div>
            <Switch
              checked={compressionConversation}
              onCheckedChange={setCompressionConversation}
            />
          </div>

          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Code</Label>
              <p className="text-muted-foreground text-xs">
                Compress code blocks in messages.
              </p>
            </div>
            <Switch
              checked={compressionCode}
              onCheckedChange={setCompressionCode}
            />
          </div>

          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>Deduplicate</Label>
              <p className="text-muted-foreground text-xs">
                Remove duplicate content across messages.
              </p>
            </div>
            <Switch
              checked={compressionDeduplicate}
              onCheckedChange={setCompressionDeduplicate}
            />
          </div>
        </CardContent>
      </Card>

      <div className="mt-6 flex gap-2">
        <Button variant="outline" onClick={reset}>
          Reset
        </Button>
        <Button onClick={() => void save()} disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </>
  );
}
