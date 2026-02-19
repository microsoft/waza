import { useMemo, useState } from "react";
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  flexRender,
  createColumnHelper,
  type SortingState,
} from "@tanstack/react-table";
import { ArrowUpDown, CheckCircle2, XCircle, AlertCircle } from "lucide-react";
import type { RunSummary } from "../api/client";
import {
  formatDuration,
  formatCost,
  formatNumber,
  formatRelativeTime,
  formatPercent,
} from "../lib/format";

function OutcomeBadge({ outcome }: { outcome: string }) {
  if (outcome === "pass")
    return <CheckCircle2 className="h-4 w-4 text-green-500" />;
  if (outcome === "fail")
    return <XCircle className="h-4 w-4 text-red-500" />;
  return <AlertCircle className="h-4 w-4 text-yellow-500" />;
}

const col = createColumnHelper<RunSummary>();

export function RunsTableSkeleton() {
  return (
    <div className="rounded-lg border border-zinc-700 bg-zinc-800">
      <div className="p-4">
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className="mb-3 flex gap-4">
            <div className="h-5 w-8 rounded bg-zinc-700" />
            <div className="h-5 w-32 rounded bg-zinc-700" />
            <div className="h-5 w-24 rounded bg-zinc-700" />
            <div className="h-5 flex-1 rounded bg-zinc-700" />
          </div>
        ))}
      </div>
    </div>
  );
}

export default function RunsTable({ data }: { data: RunSummary[] }) {
  const [sorting, setSorting] = useState<SortingState>([]);

  const columns = useMemo(
    () => [
      col.accessor("outcome", {
        header: "",
        cell: (info) => <OutcomeBadge outcome={info.getValue()} />,
        size: 40,
        enableSorting: false,
      }),
      col.accessor("spec", {
        header: "Spec",
        cell: (info) => (
          <span className="font-medium text-zinc-100">{info.getValue()}</span>
        ),
      }),
      col.accessor("model", {
        header: "Model",
        cell: (info) => (
          <span className="text-zinc-300">{info.getValue()}</span>
        ),
      }),
      col.display({
        id: "passRate",
        header: "Pass Rate",
        cell: (info) => {
          const row = info.row.original;
          const rate = row.taskCount > 0 ? row.passCount / row.taskCount : 0;
          return <span className="text-zinc-300">{formatPercent(rate)}</span>;
        },
      }),
      col.accessor("taskCount", {
        header: "Tasks",
        cell: (info) => (
          <span className="text-zinc-300">{info.getValue()}</span>
        ),
      }),
      col.accessor("tokens", {
        header: "Tokens",
        cell: (info) => (
          <span className="text-zinc-300">{formatNumber(info.getValue())}</span>
        ),
      }),
      col.accessor("cost", {
        header: "Cost",
        cell: (info) => (
          <span className="text-zinc-300">{formatCost(info.getValue())}</span>
        ),
      }),
      col.accessor("duration", {
        header: "Duration",
        cell: (info) => (
          <span className="text-zinc-300">
            {formatDuration(info.getValue())}
          </span>
        ),
      }),
      col.accessor("timestamp", {
        header: "When",
        cell: (info) => (
          <span className="text-zinc-400">
            {formatRelativeTime(info.getValue())}
          </span>
        ),
        sortingFn: "datetime",
      }),
    ],
    [],
  );

  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  return (
    <div className="overflow-x-auto rounded-lg border border-zinc-700 bg-zinc-800">
      <table className="w-full text-left text-sm">
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id} className="border-b border-zinc-700">
              {hg.headers.map((header) => (
                <th
                  key={header.id}
                  className="px-4 py-3 text-xs font-medium text-zinc-400 uppercase"
                  style={{ width: header.getSize() !== 150 ? header.getSize() : undefined }}
                >
                  {header.isPlaceholder ? null : header.column.getCanSort() ? (
                    <button
                      className="flex items-center gap-1"
                      onClick={header.column.getToggleSortingHandler()}
                    >
                      {flexRender(
                        header.column.columnDef.header,
                        header.getContext(),
                      )}
                      <ArrowUpDown className="h-3 w-3" />
                    </button>
                  ) : (
                    flexRender(
                      header.column.columnDef.header,
                      header.getContext(),
                    )
                  )}
                </th>
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row, i) => (
            <tr
              key={row.id}
              className={`cursor-pointer border-b border-zinc-700/50 ${
                i % 2 === 0 ? "bg-zinc-800" : "bg-zinc-800/60"
              } hover:bg-zinc-700/50`}
              onClick={() => {
                window.location.hash = `/runs/${row.original.id}`;
              }}
            >
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id} className="px-4 py-3">
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {data.length === 0 && (
        <div className="p-8 text-center text-zinc-500">No runs found.</div>
      )}
    </div>
  );
}
