import type { Column } from "@tanstack/react-table";

function escapeCsvCell(value: unknown): string {
  if (value === null || value === undefined) return "";
  const str = String(value);
  if (str.includes(",") || str.includes('"') || str.includes("\n")) {
    return `"${str.replace(/"/g, '""')}"`;
  }
  return str;
}

export function exportToCsv<TData>(
  filename: string,
  rows: TData[],
  columns: Column<TData, unknown>[],
) {
  const headers = columns.map((col) => col.id);
  const csv = [headers.join(",")];

  for (const row of rows) {
    csv.push(
      columns
        .map((col) => escapeCsvCell((row as Record<string, unknown>)[col.id]))
        .join(","),
    );
  }

  const blob = new Blob([csv.join("\n")], {
    type: "text/csv;charset=utf-8;",
  });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `${filename}.csv`;
  link.click();
  URL.revokeObjectURL(url);
}
