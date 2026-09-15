"use client";

import {
  Braces,
  Brain,
  Eye,
  MessageSquare,
  Radio,
  Ruler,
  Volume2,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { Capability } from "@/lib/api";

/**
 * The capability vocabulary as the dashboard presents it. The ids and their
 * order mirror internal/provider/capability.go; the icons and labels are
 * presentation only, so the server stays the single source of truth for which
 * capabilities exist.
 */
export const CAPABILITIES: {
  id: Capability;
  label: string;
  description: string;
  icon: LucideIcon;
}[] = [
  {
    id: "chat",
    label: "Chat",
    description: "Ordinary conversation.",
    icon: MessageSquare,
  },
  {
    id: "vision",
    label: "Vision",
    description: "Image input.",
    icon: Eye,
  },
  {
    id: "tools",
    label: "Tools",
    description: "Function / tool calling.",
    icon: Wrench,
  },
  {
    id: "embeddings",
    label: "Embeddings",
    description: "Serves /v1/embeddings.",
    icon: Ruler,
  },
  {
    id: "reasoning",
    label: "Reasoning",
    description: "Long reasoning model.",
    icon: Brain,
  },
  {
    id: "streaming",
    label: "Streaming",
    description: "SSE streaming.",
    icon: Radio,
  },
  {
    id: "audio",
    label: "Audio",
    description: "Audio input/output.",
    icon: Volume2,
  },
  {
    id: "json_mode",
    label: "JSON mode",
    description: "Structured output.",
    icon: Braces,
  },
];

/**
 * CapabilityBadges renders a model's capabilities in canonical order. An empty
 * set reads as "unclassified", not "supports nothing": aliases created before
 * capabilities existed carry no tags until they are edited or re-imported.
 */
export function CapabilityBadges({
  capabilities,
  compact = false,
}: {
  capabilities: Capability[];
  compact?: boolean;
}) {
  const known = CAPABILITIES.filter((c) => capabilities.includes(c.id));
  if (known.length === 0) {
    return <span className="text-muted-foreground text-xs">unclassified</span>;
  }

  return (
    <div className="flex flex-wrap gap-1">
      {known.map(({ id, label, description, icon: Icon }) => (
        <Badge key={id} variant="secondary" title={description}>
          <Icon className="size-3" />
          {compact ? null : label}
        </Badge>
      ))}
    </div>
  );
}
