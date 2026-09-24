"use client";

import Link from "next/link";
import { Puzzle } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { Extension } from "@/lib/api";
import { useResource } from "@/lib/use-resource";

const TYPE_LABELS: Record<string, string> = {
  system_prompt_injector: "System Prompt Injector",
  context_optimizer: "Context Optimizer",
};

export default function ExtensionsPage() {
  const { data, error } = useResource<Extension[]>("/extensions");
  const extensions = data ?? [];

  return (
    <>
      <PageHeader
        title="Extensions"
        description="Plugins that extend the router's behavior."
      />

      {error ? <p className="text-destructive text-sm">{error}</p> : null}

      {extensions.length === 0 ? (
        <p className="text-muted-foreground rounded-xl border border-dashed py-8 text-center text-sm">
          No extensions configured.
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {extensions.map((ext) => (
            <Card key={ext.id} className="gap-4">
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Puzzle className="size-5" />
                  {ext.name}
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="flex flex-wrap gap-1.5">
                  <Badge variant="secondary">
                    {TYPE_LABELS[ext.type] ?? ext.type}
                  </Badge>
                  <Badge variant={ext.enabled ? "secondary" : "outline"}>
                    {ext.enabled ? "enabled" : "disabled"}
                  </Badge>
                </div>
                <Button variant="outline" size="sm" asChild>
                  <Link href={`/extensions/${ext.id}`}>Details</Link>
                </Button>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </>
  );
}
