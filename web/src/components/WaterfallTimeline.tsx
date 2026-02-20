import { useState, useMemo } from "react";
import type { TranscriptEvent } from "../api/client";
import type { ToolSpan } from "../types/trajectory";
import { extractToolSpans, groupSpansByTool } from "../types/trajectory";
import TimelineAxis from "./TimelineAxis";
import Swimlane from "./Swimlane";
import DetailPanel from "./DetailPanel";

const LABEL_WIDTH = 160;

interface WaterfallTimelineProps {
  events: TranscriptEvent[];
}

export default function WaterfallTimeline({ events }: WaterfallTimelineProps) {
  const [selectedSpan, setSelectedSpan] = useState<ToolSpan | null>(null);

  const spans = useMemo(() => extractToolSpans(events), [events]);
  const grouped = useMemo(() => groupSpansByTool(spans), [spans]);

  return (
    <div className="flex rounded-lg border border-zinc-700 overflow-hidden">
      {/* Main timeline area */}
      <div className="flex-1 min-w-0 bg-zinc-900 overflow-x-auto">
        <TimelineAxis totalEvents={events.length} labelWidth={LABEL_WIDTH} />
        <div>
          {Array.from(grouped.entries()).map(([toolName, toolSpans]) => (
            <Swimlane
              key={toolName}
              toolName={toolName}
              spans={toolSpans}
              totalEvents={events.length}
              labelWidth={LABEL_WIDTH}
              selectedSpanId={selectedSpan?.id ?? null}
              onSelectSpan={setSelectedSpan}
            />
          ))}
        </div>
        {grouped.size === 0 && (
          <div className="p-6 text-center text-sm text-zinc-500">
            No tool calls found in transcript
          </div>
        )}
      </div>

      {/* Detail sidebar */}
      {selectedSpan && (
        <DetailPanel
          span={selectedSpan}
          onClose={() => setSelectedSpan(null)}
        />
      )}
    </div>
  );
}
