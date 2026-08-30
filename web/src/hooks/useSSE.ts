import { useState, useEffect, useRef, useCallback } from "react";
import { fetchRuns } from "../api/client";

export interface SSEEventData {
  spec?: string;
  model?: string;
  taskName?: string;
  outcome?: string;
  score?: number;
  duration?: number;
  graderName?: string;
  graderType?: string;
  passed?: boolean;
  message?: string;
  totalTasks?: number;
  completedTasks?: number;
  passCount?: number;
  failCount?: number;
  tokens?: number;
  cost?: number;
}

export interface SSEEvent {
  schemaVersion?: string;
  sequence?: number;
  runId?: string;
  type:
    | "run_started"
    | "task_started"
    | "step_executed"
    | "task_completed"
    | "run_completed"
    | "run_failed"
    | "task_start"
    | "task_complete"
    | "grader_result"
    | "run_complete";
  data: SSEEventData;
  timestamp: string;
}

export interface LiveRun {
  totalTasks: number;
  completedTasks: number;
  passCount: number;
  failCount: number;
  tokens: number;
  cost: number;
  currentTask: string | null;
  startTime: number;
  done: boolean;
}

interface UseSSEReturn {
  isConnected: boolean;
  currentRun: LiveRun | null;
  completedTasks: string[];
  events: SSEEvent[];
  runId: string | null;
}

const MAX_EVENTS = 200;
const BASE_DELAY = 1000;
const MAX_DELAY = 30000;

export function useSSE(): UseSSEReturn {
  const [isConnected, setIsConnected] = useState(false);
  const [currentRun, setCurrentRun] = useState<LiveRun | null>(null);
  const [completedTasks, setCompletedTasks] = useState<string[]>([]);
  const [events, setEvents] = useState<SSEEvent[]>([]);
  const [runId, setRunId] = useState<string | null>(null);
  const retryCount = useRef(0);
  const esRef = useRef<EventSource | null>(null);
  const currentRunRef = useRef<LiveRun | null>(null);
  const lastEventIdRef = useRef<string | null>(null);

  const updateCurrentRun = useCallback(
    (updater: (prev: LiveRun | null) => LiveRun | null) => {
      setCurrentRun((prev) => {
        const next = updater(prev);
        currentRunRef.current = next;
        return next;
      });
    },
    [],
  );

  const processEvent = useCallback((event: SSEEvent) => {
    setEvents((prev) => [event, ...prev].slice(0, MAX_EVENTS));

    switch (event.type) {
      case "run_started":
        setCompletedTasks([]);
        updateCurrentRun(() => ({
          totalTasks: event.data.totalTasks ?? 0,
          completedTasks: event.data.completedTasks ?? 0,
          passCount: 0,
          failCount: 0,
          tokens: 0,
          cost: 0,
          currentTask: null,
          startTime: new Date(event.timestamp).getTime(),
          done: false,
        }));
        break;

      case "task_started":
      case "task_start":
        if (!currentRunRef.current || currentRunRef.current.done) {
          setCompletedTasks([]);
        }
        updateCurrentRun((prev) => {
          // Reset state if previous run finished or no run exists
          const base =
            !prev || prev.done
              ? {
                  totalTasks: 0,
                  completedTasks: 0,
                  passCount: 0,
                  failCount: 0,
                  tokens: 0,
                  cost: 0,
                  currentTask: null,
                  startTime: Date.now(),
                  done: false,
                }
              : prev;
          return { ...base, currentTask: event.data.taskName ?? null };
        });
        break;

      case "task_completed":
      case "task_complete":
        updateCurrentRun((prev) => {
          if (!prev) return prev;
          const passed = event.data.outcome?.startsWith("pass") ?? false;
          return {
            ...prev,
            completedTasks: prev.completedTasks + 1,
            passCount: prev.passCount + (passed ? 1 : 0),
            failCount: prev.failCount + (passed ? 0 : 1),
            currentTask: null,
          };
        });
        if (event.data.taskName) {
          setCompletedTasks((prev) => [...prev, event.data.taskName!]);
        }
        break;

      case "step_executed":
      case "grader_result":
        // Informational — no state change needed
        break;

      case "run_failed":
      case "run_completed":
      case "run_complete":
        updateCurrentRun((prev) => {
          if (!prev) return prev;
          const totalTasks = event.data.totalTasks ?? prev.totalTasks;
          const passCount = event.data.passCount ?? prev.passCount;
          return {
            ...prev,
            totalTasks,
            completedTasks: event.data.completedTasks ?? totalTasks,
            passCount,
            failCount: event.data.failCount ?? Math.max(totalTasks - passCount, 0),
            tokens: event.data.tokens ?? prev.tokens,
            cost: event.data.cost ?? prev.cost,
            currentTask: null,
            done: true,
          };
        });
        break;
    }
  }, [updateCurrentRun]);

  useEffect(() => {
    let cancelled = false;

    async function loadLatestRun() {
      try {
        const runs = await fetchRuns("timestamp", "desc");
        if (!cancelled) {
          setRunId(runs[0]?.id ?? null);
        }
      } catch (error) {
        console.warn("Failed to load latest run for live events", error);
        if (!cancelled) setIsConnected(false);
      }
    }

    void loadLatestRun();

    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!runId) return;
    const currentRunId = runId;
    lastEventIdRef.current = null;

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    function connect() {
      if (cancelled) return;

      const baseURL = `/api/v1/runs/${encodeURIComponent(currentRunId)}/events`;
      const url = lastEventIdRef.current
        ? `${baseURL}?lastEventId=${encodeURIComponent(lastEventIdRef.current)}`
        : baseURL;
      const es = new EventSource(url);
      esRef.current = es;

      es.onopen = () => {
        if (cancelled) return;
        setIsConnected(true);
        retryCount.current = 0;
      };

      es.onmessage = (msg) => {
        if (cancelled) return;
        try {
          const parsed = JSON.parse(msg.data) as SSEEvent;
          processEvent(parsed);
          lastEventIdRef.current =
            msg.lastEventId || parsed.sequence?.toString() || lastEventIdRef.current;
          if (
            parsed.type === "run_completed" ||
            parsed.type === "run_failed" ||
            parsed.type === "run_complete"
          ) {
            es.close();
            esRef.current = null;
            setIsConnected(false);
          }
        } catch (error) {
          console.warn("Failed to parse live event", error);
        }
      };

      es.onerror = () => {
        if (cancelled) return;
        es.close();
        esRef.current = null;
        setIsConnected(false);

        const delay = Math.min(
          BASE_DELAY * Math.pow(2, retryCount.current),
          MAX_DELAY,
        );
        retryCount.current += 1;
        timer = setTimeout(connect, delay);
      };
    }

    connect();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      if (esRef.current) {
        esRef.current.close();
        esRef.current = null;
      }
      setIsConnected(false);
    };
  }, [processEvent, runId]);

  return { isConnected, currentRun, completedTasks, events, runId };
}
