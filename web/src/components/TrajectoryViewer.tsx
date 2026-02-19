import { useState } from "react";
import {
  Wrench,
  FileText,
  Brain,
  Terminal,
  ChevronDown,
  ChevronRight,
} from "lucide-react";
import type { TaskResult, GraderResult } from "../api/client";

interface TrajectoryEvent {
  type: "tool_call" | "file_edit" | "reasoning" | "output";
  timestamp: string;
  description: string;
  content?: string;
}

const iconMap: Record<
  TrajectoryEvent["type"],
  React.ComponentType<{ className?: string }>
> = {
  tool_call: Wrench,
  file_edit: FileText,
  reasoning: Brain,
  output: Terminal,
};

const dotColor: Record<TrajectoryEvent["type"], string> = {
  tool_call: "bg-blue-500",
  file_edit: "bg-green-500",
  reasoning: "bg-purple-500",
  output: "bg-yellow-500",
};

const badgeStyle: Record<TrajectoryEvent["type"], string> = {
  tool_call: "bg-blue-500/10 text-blue-400",
  file_edit: "bg-green-500/10 text-green-400",
  reasoning: "bg-purple-500/10 text-purple-400",
  output: "bg-yellow-500/10 text-yellow-400",
};

function parseTrajectory(task: TaskResult): TrajectoryEvent[] {
  const events: TrajectoryEvent[] = [];
  const now = new Date().toISOString();

  // Extract trajectory info from grader results messages
  for (const g of task.graderResults) {
    if (g.message) {
      // Detect file edits
      if (/\b(edit|creat|modif|writ|updat)\w*\b.*\.(ts|js|go|py|md|json|yaml|yml)\b/i.test(g.message)) {
        events.push({
          type: "file_edit",
          timestamp: now,
          description: g.message,
          content: undefined,
        });
      }
      // Detect tool calls
      else if (/\b(tool|command|run|exec|invoke|call)\b/i.test(g.message)) {
        events.push({
          type: "tool_call",
          timestamp: now,
          description: g.message,
          content: undefined,
        });
      }
      // Everything else is reasoning/output
      else {
        events.push({
          type: "reasoning",
          timestamp: now,
          description: g.message,
          content: undefined,
        });
      }
    }
  }

  // If no events were extracted, create a summary
  if (events.length === 0) {
    events.push({
      type: "output",
      timestamp: now,
      description: `Task "${task.name}" completed with outcome: ${task.outcome}`,
      content: `Score: ${Math.round(task.score * 100)}%\nDuration: ${Math.round(task.duration)}s\nGraders: ${task.graderResults.length}`,
    });
  }

  return events;
}

function parseSingleGrader(grader: GraderResult): TrajectoryEvent[] {
  const events: TrajectoryEvent[] = [];
  const now = new Date().toISOString();

  events.push({
    type: "tool_call",
    timestamp: now,
    description: `Grader: ${grader.name} (${grader.type})`,
    content: `Passed: ${grader.passed ? "yes" : "no"}\nScore: ${Math.round(grader.score * 100)}%\nMessage: ${grader.message}`,
  });

  return events;
}

function TimelineEvent({ event }: { event: TrajectoryEvent }) {
  const [expanded, setExpanded] = useState(false);
  const Icon = iconMap[event.type];

  return (
    <div className="relative flex gap-3 pb-6 last:pb-0">
      {/* vertical line */}
      <div className="flex flex-col items-center">
        <div className={`h-3 w-3 rounded-full ${dotColor[event.type]}`} />
        <div className="w-px flex-1 bg-zinc-600" />
      </div>

      {/* content */}
      <div className="-mt-0.5 flex-1 space-y-1">
        <div className="flex items-center gap-2">
          <span
            className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium ${badgeStyle[event.type]}`}
          >
            <Icon className="h-3 w-3" />
            {event.type.replace("_", " ")}
          </span>
          <span className="text-xs text-zinc-500">
            {new Date(event.timestamp).toLocaleTimeString()}
          </span>
        </div>

        <p className="text-sm text-zinc-300">{event.description}</p>

        {event.content && (
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300"
          >
            {expanded ? (
              <ChevronDown className="h-3 w-3" />
            ) : (
              <ChevronRight className="h-3 w-3" />
            )}
            {expanded ? "Hide" : "Show"} details
          </button>
        )}

        {expanded && event.content && (
          <pre className="mt-1 overflow-x-auto rounded-lg bg-zinc-900 p-3 text-xs text-zinc-300">
            <code>{event.content}</code>
          </pre>
        )}
      </div>
    </div>
  );
}

export default function TrajectoryViewer({ task }: { task: TaskResult }) {
  const events = parseTrajectory(task);
  const graderEvents = task.graderResults.flatMap(parseSingleGrader);
  const allEvents = [...events, ...graderEvents];

  // dedupe by description
  const seen = new Set<string>();
  const unique = allEvents.filter((e) => {
    if (seen.has(e.description)) return false;
    seen.add(e.description);
    return true;
  });

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-medium text-zinc-400">
        Trajectory — {task.name}
      </h3>

      {unique.length === 0 ? (
        <p className="text-sm text-zinc-500">
          No trajectory data available for this task.
        </p>
      ) : (
        <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
          {unique.map((event, i) => (
            <TimelineEvent key={`${event.type}-${i}`} event={event} />
          ))}
        </div>
      )}
    </div>
  );
}
