import { useState, useEffect, useRef, useCallback } from "react";

export interface SSEEventData {
  taskName?: string;
  outcome?: string;
  score?: number;
  duration?: number;
  graderName?: string;
  graderType?: string;
  passed?: boolean;
  message?: string;
  totalTasks?: number;
  passCount?: number;
  tokens?: number;
  cost?: number;
}

export interface SSEEvent {
  type: "task_start" | "task_complete" | "grader_result" | "run_complete";
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
}

const MAX_EVENTS = 200;
const BASE_DELAY = 1000;
const MAX_DELAY = 30000;

export function useSSE(): UseSSEReturn {
  const [isConnected, setIsConnected] = useState(false);
  const [currentRun, setCurrentRun] = useState<LiveRun | null>(null);
  const [completedTasks, setCompletedTasks] = useState<string[]>([]);
  const [events, setEvents] = useState<SSEEvent[]>([]);
  const retryCount = useRef(0);
  const esRef = useRef<EventSource | null>(null);

  const processEvent = useCallback((event: SSEEvent) => {
    setEvents((prev) => [event, ...prev].slice(0, MAX_EVENTS));

    switch (event.type) {
      case "task_start":
        setCurrentRun((prev) => {
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
        // Clear completed tasks list when a new run starts
        setCompletedTasks((prev) =>
          currentRun?.done ? [] : prev,
        );
        break;

      case "task_complete":
        setCurrentRun((prev) => {
          if (!prev) return prev;
          const passed = event.data.outcome === "pass";
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

      case "grader_result":
        // Informational — no state change needed
        break;

      case "run_complete":
        setCurrentRun((prev) => {
          if (!prev) return prev;
          return {
            ...prev,
            totalTasks: event.data.totalTasks ?? prev.totalTasks,
            passCount: event.data.passCount ?? prev.passCount,
            tokens: event.data.tokens ?? prev.tokens,
            cost: event.data.cost ?? prev.cost,
            currentTask: null,
            done: true,
          };
        });
        break;
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    function connect() {
      if (cancelled) return;

      const es = new EventSource("/api/events");
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
        } catch {
          // ignore malformed events
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
  }, [processEvent]);

  return { isConnected, currentRun, completedTasks, events };
}
