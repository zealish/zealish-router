"use client";

import type { Table } from "@tanstack/react-table";
import { Download, Search, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { exportToCsv } from "./data-table-export";

export type DataTableFilterOption = { label: string; value: string };

export type DataTableFilter = {
  columnId: string;
  title: string;
  options: DataTableFilterOption[];
};

export function DataTableToolbar<TData>({
  table,
  searchKey,
  searchPlaceholder,
  filters,
  enableExport = false,
  exportFilename = "export",
  renderBulkActions,
  children,
}: {
  table: Table<TData>;
  searchKey?: string;
  searchPlaceholder?: string;
  filters?: DataTableFilter[];
  enableExport?: boolean;
  exportFilename?: string;
  renderBulkActions?: (rows: TData[]) => React.ReactNode;
  children?: React.ReactNode;
}) {
  "use no memo";

  const isFiltered = table.getState().columnFilters.length > 0;
  const selectedRows = table.getFilteredSelectedRowModel().rows;

  const searchColumn = searchKey ? table.getColumn(searchKey) : undefined;
  const hasControls = Boolean(
    searchColumn || filters?.length || enableExport || children,
  );
  if (!hasControls && selectedRows.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex flex-1 flex-wrap items-center gap-2">
        {searchColumn ? (
          <div className="relative w-full sm:w-64">
            <Search className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2" />
            <Input
              value={(searchColumn.getFilterValue() as string) ?? ""}
              placeholder={searchPlaceholder ?? "Search…"}
              className="pl-9"
              onChange={(e) => searchColumn.setFilterValue(e.target.value)}
            />
          </div>
        ) : null}

        {filters?.map((filter) => {
          const column = table.getColumn(filter.columnId);
          if (!column) return null;
          return (
            <Select
              key={filter.columnId}
              value={(column.getFilterValue() as string) ?? "__all"}
              onValueChange={(value) =>
                column.setFilterValue(value === "__all" ? undefined : value)
              }
            >
              <SelectTrigger size="sm" className="w-40">
                <SelectValue placeholder={filter.title} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__all">{filter.title}</SelectItem>
                {filter.options.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          );
        })}

        {isFiltered ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => table.resetColumnFilters()}
          >
            Reset
            <X />
          </Button>
        ) : null}
      </div>

      <div className="flex items-center gap-2">
        {selectedRows.length > 0 && renderBulkActions
          ? renderBulkActions(selectedRows.map((row) => row.original))
          : null}
        {children}
        {enableExport ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() =>
              exportToCsv(
                exportFilename,
                table.getFilteredRowModel().rows.map((row) => row.original),
                table
                  .getAllColumns()
                  .filter((col) => col.id !== "select" && col.getIsVisible()),
              )
            }
          >
            <Download />
            Export
          </Button>
        ) : null}
      </div>
    </div>
  );
}
