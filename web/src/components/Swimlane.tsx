import { useState } from "react";
import type { ToolSpan } from "../types/trajectory";

const statusColor: Record<ToolSpan["status"], string> = {
  pass: "bg-green-500 hover:bg-green-400",
  fail: "bg-red-500 hover:bg-red-400",
  pending: "bg-blue-500 hover:bg-blue-400",
};

interface SwimlaneProps {
  toolName: string;
  spans: ToolSpan[];
  totalEvents: number;
  labelWidth: number;
  selectedSpanId: string | null;
  onSelectSpan: (span: ToolSpan) => void;
}

export default function Swimlane({
  toolName,
  spans,
  totalEvents,
  labelWidth,
  selectedSpanId,
  onSelectSpan,
}: SwimlaneProps) {
  const [hoveredId, setHoveredId] = useState<string | null>(null);

  return (
    <div className="flex items-stretch border-b border-zinc-700 last:border-b-0 group">
      {/* Tool name label */}
      <div
        className="shrink-0 flex items-center px-3 border-r border-zinc-700 bg-zinc-800"
        style={{ width: labelWidth }}
      >
        <span className="text-xs font-mono text-zinc-300 truncate">
          {toolName}
        </span>
      </div>

      {/* Bar area */}
      <div className="relative flex-1 h-8">
        {spans.map((span) => {
          const left =
            totalEvents > 0 ? (span.startIndex / totalEvents) * 100 : 0;
          // Minimum width of 1% so single-event spans are visible
          const width =
            totalEvents > 0
              ? Math.max((span.duration / totalEvents) * 100, 1)
              : 1;
          const isSelected = span.id === selectedSpanId;
          const isHovered = span.id === hoveredId;

          return (
            <div key={span.id} className="absolute top-1 bottom-1">
              <button
                className={`h-full rounded-sm cursor-pointer transition-opacity ${statusColor[span.status]} ${
                  isSelected ? "ring-2 ring-white/60" : ""
                }`}
                style={{
                  position: "absolute",
                  left: `${left}%`,
                  width: `${width}%`,
                  minWidth: "6px",
                }}
                onClick={() => onSelectSpan(span)}
                onMouseEnter={() => setHoveredId(span.id)}
                onMouseLeave={() => setHoveredId(null)}
                aria-label={`${span.toolName} — ${span.status}`}
              />

              {/* Tooltip */}
              {isHovered && (
                <div
                  className="absolute z-20 -top-8 px-2 py-1 rounded bg-zinc-700 text-xs text-zinc-200 whitespace-nowrap pointer-events-none"
                  style={{ left: `${left}%` }}
                >
                  {span.toolName} — {span.duration} events
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
