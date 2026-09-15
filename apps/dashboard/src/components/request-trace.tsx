"use client";

import { Badge } from "@/components/ui/badge";

/**
 * Status tone: a successful attempt is unremarkable, a rejected one is the
 * caller's problem, and everything else is an upstream failure worth spotting.
 */
function statusVariant(
  status: string,
): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "ok":
      return "default";
    case "client_error":
    case "canceled":
      return "secondary";
    case "circuit_open":
    case "unhealthy":
      return "outline";
    default:
      return "destructive";
  }
}

export function StatusBadge({ status }: { status: string }) {
  if (!status) return <span className="text-muted-foreground">—</span>;
  return (
    <Badge variant={statusVariant(status)} className="font-mono">
      {status}
    </Badge>
  );
}

/**
 * Which dialect the caller spoke. Both reach the same aliases, so this is a
 * quiet outline badge rather than anything that reads as a status.
 */
export function DialectBadge({ dialect }: { dialect: string }) {
  if (!dialect) return <span className="text-muted-foreground">—</span>;
  return (
    <Badge variant="outline" className="font-mono">
      {dialect}
    </Badge>
  );
}

/** Latencies read in ms until they pass a second. */
export function formatLatency(ms: number): string {
  if (ms <= 0) return "—";
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(2)} s`;
}

/** Costs are often fractions of a cent, so small values keep more digits. */
export function formatCost(n: number): string {
  if (n === 0) return "$0.00";
  if (n < 0.01) return `$${n.toFixed(5)}`;
  if (n < 1) return `$${n.toFixed(4)}`;
  return `$${n.toFixed(2)}`;
}
