import { useEffect, useState } from "react";
import { X, CheckCircle2, XCircle, Clock, ChevronDown, ChevronRight } from "lucide-react";
import type { ToolSpan } from "../types/trajectory";

function JsonViewer({ label, data }: { label: string; data: unknown }) {
  const [open, setOpen] = useState(false);
  if (data === undefined || data === null) return null;

  const text =
    typeof data === "string" ? data : JSON.stringify(data, null, 2);

  return (
    <div>
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300"
      >
        {open ? (
          <ChevronDown className="h-3 w-3" />
        ) : (
          <ChevronRight className="h-3 w-3" />
        )}
        {label}
      </button>
      {open && (
        <pre className="mt-1 max-h-64 overflow-auto rounded bg-zinc-900 p-2.5 text-xs text-zinc-300">
          <code>{text}</code>
        </pre>
      )}
    </div>
  );
}

const statusBadge: Record<ToolSpan["status"], string> = {
  pass: "bg-green-500/10 text-green-400",
  fail: "bg-red-500/10 text-red-400",
  pending: "bg-blue-500/10 text-blue-400",
};

interface DetailPanelProps {
  span: ToolSpan;
  onClose: () => void;
}

export default function DetailPanel({ span, onClose }: DetailPanelProps) {
  // Escape key handler
  useEffect(() => {
    function handleKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", handleKey);
    return () => window.removeEventListener("keydown", handleKey);
  }, [onClose]);

  return (
    <div className="w-80 shrink-0 border-l border-zinc-700 bg-zinc-800 overflow-y-auto">
      {/* Header */}
      <div className="flex items-center justify-between p-3 border-b border-zinc-700">
        <h4 className="text-sm font-medium text-zinc-200 truncate">
          {span.toolName}
        </h4>
        <button
          onClick={onClose}
          className="p-1 rounded hover:bg-zinc-700 text-zinc-400 hover:text-zinc-200"
          aria-label="Close detail panel"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Body */}
      <div className="p-3 space-y-4">
        {/* Status */}
        <div className="flex items-center gap-2">
          {span.status === "pass" ? (
            <CheckCircle2 className="h-4 w-4 text-green-400" />
          ) : span.status === "fail" ? (
            <XCircle className="h-4 w-4 text-red-400" />
          ) : (
            <Clock className="h-4 w-4 text-blue-400" />
          )}
          <span
            className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${statusBadge[span.status]}`}
          >
            {span.status}
          </span>
        </div>

        {/* Metadata */}
        <div className="space-y-2 text-xs">
          <div className="flex justify-between">
            <span className="text-zinc-500">Call ID</span>
            <span className="font-mono text-zinc-300 truncate max-w-40">
              {span.toolCallId}
            </span>
          </div>
          <div className="flex justify-between">
            <span className="text-zinc-500">Event range</span>
            <span className="text-zinc-300">
              {span.startIndex} → {span.endIndex}
            </span>
          </div>
          <div className="flex justify-between">
            <span className="text-zinc-500">Duration</span>
            <span className="text-zinc-300">{span.duration} events</span>
          </div>
        </div>

        {/* Arguments & Result */}
        <div className="space-y-2">
          <JsonViewer label="Arguments" data={span.arguments} />
          <JsonViewer label="Result" data={span.toolResult} />
        </div>
      </div>
    </div>
  );
}
