"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { ChevronLeft, ChevronRight, RotateCw, Search } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  REQUEST_DIALECTS,
  REQUEST_STATUSES,
  type ModelAlias,
  type Provider,
  type RequestList,
} from "@/lib/api";
import { useResource } from "@/lib/use-resource";
import {
  DialectBadge,
  formatCost,
  formatLatency,
  StatusBadge,
} from "@/components/request-trace";

/** Newest requests arrive constantly, so the list refreshes on its own. */
const POLL_MS = 10_000;
const PAGE_SIZE = 25;

type Filters = {
  status: string;
  model: string;
  provider: string;
  dialect: string;
  api_key: string;
};

const NO_FILTERS: Filters = {
  status: "",
  model: "",
  provider: "",
  dialect: "",
  api_key: "",
};

/** Builds the querystring the admin API expects, omitting empty filters. */
function buildQuery(filters: Filters, offset: number): string {
  const params = new URLSearchParams({
    limit: String(PAGE_SIZE),
    offset: String(offset),
  });
  for (const [key, value] of Object.entries(filters)) {
    if (value) params.set(key, value);
  }
  return `/requests?${params.toString()}`;
}

export default function RequestsPage() {
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const [keyDraft, setKeyDraft] = useState("");
  const [offset, setOffset] = useState(0);

  const path = useMemo(() => buildQuery(filters, offset), [filters, offset]);
  const { data, error, loading, reload } = useResource<RequestList>(
    path,
    POLL_MS,
  );
  const models = useResource<ModelAlias[]>("/models");
  const providers = useResource<Provider[]>("/providers");

  // Any filter change invalidates the current page: the result set moved.
  const setFilter = (key: keyof Filters, value: string) => {
    setFilters((prev) => ({ ...prev, [key]: value }));
    setOffset(0);
  };

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const filtered = Object.values(filters).some(Boolean);
  const page = Math.floor(offset / PAGE_SIZE) + 1;
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <>
      <PageHeader
        title="Request trace"
        description="Every gateway request, with the providers its chain tried before answering. Metadata only — no prompts or completions are stored."
        action={
          <Button variant="outline" size="sm" onClick={() => void reload()}>
            <RotateCw className="size-4" />
            Refresh
          </Button>
        }
      />

      {error ? <p className="text-destructive mb-4 text-sm">{error}</p> : null}

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Select
          value={filters.status || "__all"}
          onValueChange={(v) => setFilter("status", v === "__all" ? "" : v)}
        >
          <SelectTrigger size="sm" className="w-40">
            <SelectValue placeholder="Status" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all">All statuses</SelectItem>
            {REQUEST_STATUSES.map((status) => (
              <SelectItem key={status} value={status}>
                {status}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={filters.model || "__all"}
          onValueChange={(v) => setFilter("model", v === "__all" ? "" : v)}
        >
          <SelectTrigger size="sm" className="w-48">
            <SelectValue placeholder="Model" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all">All models</SelectItem>
            {(models.data ?? []).map((m) => (
              <SelectItem key={m.alias} value={m.alias}>
                {m.alias}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={filters.provider || "__all"}
          onValueChange={(v) => setFilter("provider", v === "__all" ? "" : v)}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder="Provider" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all">All providers</SelectItem>
            {(providers.data ?? []).map((p) => (
              <SelectItem key={p.name} value={p.name}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={filters.dialect || "__all"}
          onValueChange={(v) => setFilter("dialect", v === "__all" ? "" : v)}
        >
          <SelectTrigger size="sm" className="w-40">
            <SelectValue placeholder="Dialect" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all">All dialects</SelectItem>
            {REQUEST_DIALECTS.map((dialect) => (
              <SelectItem key={dialect} value={dialect}>
                {dialect}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <form
          className="relative w-full sm:w-56"
          onSubmit={(e) => {
            e.preventDefault();
            setFilter("api_key", keyDraft.trim());
          }}
        >
          <Search className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2" />
          <Input
            value={keyDraft}
            placeholder="API key id…"
            className="h-8 pl-9"
            onChange={(e) => setKeyDraft(e.target.value)}
          />
        </form>

        {filtered ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setFilters(NO_FILTERS);
              setKeyDraft("");
              setOffset(0);
            }}
          >
            Reset
          </Button>
        ) : null}
      </div>

      <div className="bg-card dark:backdrop-blur-sm dark:backdrop-saturate-125 overflow-hidden rounded-xl border [&_td:first-child]:pl-4 [&_td:last-child]:pr-4 [&_th:first-child]:pl-4 [&_th:last-child]:pr-4 [&_td]:py-3 [&_th]:h-11">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow className="hover:bg-transparent">
              <TableHead>Time</TableHead>
              <TableHead>Request</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Dialect</TableHead>
              <TableHead>Provider</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="text-right">Attempts</TableHead>
              <TableHead className="text-right">Latency</TableHead>
              <TableHead className="text-right">Tokens</TableHead>
              <TableHead className="text-right">Cost</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && items.length === 0 ? (
              Array.from({ length: 5 }).map((_, i) => (
                <TableRow key={i}>
                  <TableCell colSpan={10}>
                    <Skeleton className="h-5 w-full" />
                  </TableCell>
                </TableRow>
              ))
            ) : items.length === 0 ? (
              <TableRow>
                <TableCell
                  colSpan={10}
                  className="text-muted-foreground h-24 text-center"
                >
                  {filtered
                    ? "No requests match these filters."
                    : "No requests traced yet."}
                </TableCell>
              </TableRow>
            ) : (
              items.map((trace) => (
                <TableRow key={trace.request_id} className="cursor-pointer">
                  <TableCell className="text-muted-foreground whitespace-nowrap">
                    <Link
                      href={`/requests/${trace.request_id}`}
                      className="block"
                    >
                      {new Date(trace.created_at).toLocaleTimeString()}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Link
                      href={`/requests/${trace.request_id}`}
                      className="font-mono text-xs hover:underline"
                    >
                      {trace.request_id.slice(0, 12)}
                    </Link>
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {trace.model}
                    {trace.streamed ? (
                      <span className="text-muted-foreground ml-2">stream</span>
                    ) : null}
                  </TableCell>
                  <TableCell>
                    <DialectBadge dialect={trace.dialect} />
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {trace.final_provider || "—"}
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={trace.final_status} />
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {trace.attempt_count > 1 ? (
                      <Badge variant="outline">{trace.attempt_count}</Badge>
                    ) : (
                      trace.attempt_count
                    )}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatLatency(trace.total_latency_ms)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {trace.total_tokens.toLocaleString()}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatCost(trace.total_cost_usd)}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>

      <div className="mt-4 flex items-center justify-between gap-4">
        <span className="text-muted-foreground text-sm">
          {total.toLocaleString()} request{total === 1 ? "" : "s"}
        </span>
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground text-sm">
            Page {page} of {pages}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
          >
            <ChevronLeft className="size-4" />
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={offset + PAGE_SIZE >= total}
            onClick={() => setOffset(offset + PAGE_SIZE)}
          >
            <ChevronRight className="size-4" />
          </Button>
        </div>
      </div>
    </>
  );
}
