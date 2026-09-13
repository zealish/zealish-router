"use client";

import {
  flexRender,
  getCoreRowModel,
  getFacetedRowModel,
  getFacetedUniqueValues,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type ColumnFiltersState,
  type SortingState,
  type VisibilityState,
} from "@tanstack/react-table";
import { useState } from "react";
import { cn } from "cn";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { DataTablePagination } from "./data-table-pagination";
import { DataTableToolbar, type DataTableFilter } from "./data-table-toolbar";

export type DataTableColumnMeta = { className?: string };

export function DataTable<TData>({
  columns,
  data,
  empty = "Nothing here yet.",
  searchKey,
  searchPlaceholder,
  filters,
  enableSelection = false,
  enableExport = false,
  exportFilename,
  enablePagination = true,
  pageSizeOptions,
  defaultPageSize = 10,
  defaultSorting,
  renderBulkActions,
  toolbarActions,
}: {
  columns: ColumnDef<TData, unknown>[];
  data: TData[];
  empty?: string;
  searchKey?: string;
  searchPlaceholder?: string;
  filters?: DataTableFilter[];
  enableSelection?: boolean;
  enableExport?: boolean;
  exportFilename?: string;
  enablePagination?: boolean;
  pageSizeOptions?: number[];
  defaultPageSize?: number;
  defaultSorting?: SortingState;
  renderBulkActions?: (rows: TData[]) => React.ReactNode;
  toolbarActions?: React.ReactNode;
}) {
  "use no memo";

  const [sorting, setSorting] = useState<SortingState>(defaultSorting ?? []);
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([]);
  const [columnVisibility, setColumnVisibility] = useState<VisibilityState>({});
  const [rowSelection, setRowSelection] = useState({});

  const selectionColumn: ColumnDef<TData, unknown> = {
    id: "select",
    header: ({ table }) => (
      <input
        type="checkbox"
        className="accent-primary size-4 align-middle"
        aria-label="Select all"
        checked={table.getIsAllPageRowsSelected()}
        onChange={(e) => table.toggleAllPageRowsSelected(e.target.checked)}
      />
    ),
    cell: ({ row }) => (
      <input
        type="checkbox"
        className="accent-primary size-4 align-middle"
        aria-label="Select row"
        checked={row.getIsSelected()}
        onChange={(e) => row.toggleSelected(e.target.checked)}
      />
    ),
    enableSorting: false,
    enableHiding: false,
  };

  const tableColumns = enableSelection
    ? [selectionColumn, ...columns]
    : columns;

  const table = useReactTable({
    data,
    columns: tableColumns,
    state: { sorting, columnFilters, columnVisibility, rowSelection },
    enableRowSelection: enableSelection,
    onSortingChange: setSorting,
    onColumnFiltersChange: setColumnFilters,
    onColumnVisibilityChange: setColumnVisibility,
    onRowSelectionChange: setRowSelection,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getFacetedRowModel: getFacetedRowModel(),
    getFacetedUniqueValues: getFacetedUniqueValues(),
    ...(enablePagination
      ? {
          getPaginationRowModel: getPaginationRowModel(),
          initialState: { pagination: { pageIndex: 0, pageSize: defaultPageSize } },
        }
      : {}),
  });

  return (
    <div className="space-y-4">
      <DataTableToolbar
        table={table}
        searchKey={searchKey}
        searchPlaceholder={searchPlaceholder}
        filters={filters}
        enableExport={enableExport}
        exportFilename={exportFilename}
        renderBulkActions={renderBulkActions}
      >
        {toolbarActions}
      </DataTableToolbar>

      <div className="bg-card dark:backdrop-blur-sm dark:backdrop-saturate-125 overflow-hidden rounded-xl border [&_td:first-child]:pl-4 [&_td:last-child]:pr-4 [&_th:first-child]:pl-4 [&_th:last-child]:pr-4 [&_td]:py-3 [&_th]:h-11">
        <Table>
          <TableHeader className="bg-muted/50">
            {table.getHeaderGroups().map((group) => (
              <TableRow key={group.id} className="hover:bg-transparent">
                {group.headers.map((header) => {
                  const meta = header.column.columnDef
                    .meta as DataTableColumnMeta | undefined;
                  return (
                    <TableHead
                      key={header.id}
                      colSpan={header.colSpan}
                      className={cn(meta?.className)}
                    >
                      {header.isPlaceholder
                        ? null
                        : flexRender(
                            header.column.columnDef.header,
                            header.getContext(),
                          )}
                    </TableHead>
                  );
                })}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {table.getRowModel().rows.length === 0 ? (
              <TableRow>
                <TableCell
                  colSpan={tableColumns.length}
                  className="text-muted-foreground h-24 text-center"
                >
                  {empty}
                </TableCell>
              </TableRow>
            ) : (
              table.getRowModel().rows.map((row) => (
                <TableRow
                  key={row.id}
                  data-state={row.getIsSelected() ? "selected" : undefined}
                >
                  {row.getVisibleCells().map((cell) => {
                    const meta = cell.column.columnDef.meta as
                      | DataTableColumnMeta
                      | undefined;
                    return (
                      <TableCell key={cell.id} className={cn(meta?.className)}>
                        {flexRender(
                          cell.column.columnDef.cell,
                          cell.getContext(),
                        )}
                      </TableCell>
                    );
                  })}
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>

      {enablePagination ? (
        <DataTablePagination
          table={table}
          pageSizeOptions={pageSizeOptions}
          enableSelection={enableSelection}
        />
      ) : null}
    </div>
  );
}
