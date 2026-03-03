import type { Page } from "@playwright/test";

/**
 * Mock API responses that match the Go server's JSON shape.
 * Keeping this in one place so every spec file shares the same data.
 */

export const SUMMARY = {
  totalRuns: 12,
  totalTasks: 48,
  passRate: 85,
  avgTokens: 15230,
  avgCost: 1.47,
  avgDuration: 42,
};

export const RUNS = [
  {
    id: "run-001",
    spec: "code-explainer",
    model: "gpt-4o",
    outcome: "pass",
    passCount: 4,
    taskCount: 4,
    tokens: 12400,
    cost: 1.24,
    duration: 38,
    weightedScore: 0.92,
    timestamp: new Date(Date.now() - 3600_000).toISOString(), // 1 hour ago
  },
  {
    id: "run-002",
    spec: "skill-checker",
    model: "claude-sonnet-4",
    judgeModel: "claude-opus-4.6",
    outcome: "fail",
    passCount: 2,
    taskCount: 5,
    tokens: 18100,
    cost: 1.81,
    duration: 55,
    weightedScore: 0.45,
    timestamp: new Date(Date.now() - 7200_000).toISOString(), // 2 hours ago
  },
  {
    id: "run-003",
    spec: "doc-writer",
    model: "gpt-4o",
    outcome: "pass",
    passCount: 3,
    taskCount: 3,
    tokens: 9800,
    cost: 0.98,
    duration: 27,
    timestamp: new Date(Date.now() - 86400_000).toISOString(), // 1 day ago
  },
];

export const RUN_DETAIL = {
  ...RUNS[0],
  tasks: [
    {
      name: "explain-fibonacci",
      outcome: "pass",
      score: 1.0,
      weightedScore: 1.0,
      duration: 12,
      bootstrapCI: { lower: 0.82, upper: 0.98, mean: 0.91, confidenceLevel: 0.95 },
      isSignificant: true,
      graderResults: [
        {
          name: "output-exists",
          type: "code",
          passed: true,
          score: 1.0,
          weight: 1.0,
          message: "Output file found",
        },
        {
          name: "mentions-recursion",
          type: "text",
          passed: true,
          score: 1.0,
          weight: 2.0,
          message: 'Matched pattern "recursion|recursive"',
        },
      ],
      transcript: [
        { type: "Turn", content: "I'll analyze the code now." },
        { type: "ToolExecutionStart", toolCallId: "tc-1", toolName: "read_file", arguments: { path: "fibonacci.py" } },
        { type: "ToolExecutionComplete", toolCallId: "tc-1", toolResult: "def fib(n): ...", success: true },
        { type: "ToolExecutionStart", toolCallId: "tc-2", toolName: "write_file", arguments: { path: "explanation.md", content: "# Fibonacci" } },
        { type: "ToolExecutionComplete", toolCallId: "tc-2", toolResult: "File written", success: true },
        { type: "Error", message: "Rate limit exceeded" },
      ],
      sessionDigest: {
        totalTurns: 3,
        toolCallCount: 2,
        tokensIn: 4500,
        tokensOut: 2100,
        tokensTotal: 6600,
        toolsUsed: ["read_file", "write_file"],
        errors: ["Rate limit exceeded"],
      },
    },
    {
      name: "explain-quicksort",
      outcome: "pass",
      score: 1.0,
      weightedScore: 1.0,
      duration: 10,
      bootstrapCI: { lower: 0.75, upper: 0.95, mean: 0.85, confidenceLevel: 0.95 },
      isSignificant: true,
      graderResults: [
        {
          name: "output-exists",
          type: "code",
          passed: true,
          score: 1.0,
          weight: 1.0,
          message: "Output file found",
        },
      ],
    },
    {
      name: "explain-binary-search",
      outcome: "fail",
      score: 0.0,
      weightedScore: 0.33,
      duration: 8,
      bootstrapCI: { lower: -0.05, upper: 0.15, mean: 0.05, confidenceLevel: 0.95 },
      isSignificant: false,
      graderResults: [
        {
          name: "output-exists",
          type: "code",
          passed: true,
          score: 1.0,
          weight: 1.0,
          message: "Output file found",
        },
        {
          name: "mentions-divide-conquer",
          type: "text",
          passed: false,
          score: 0.0,
          weight: 2.0,
          message: "Pattern not matched",
        },
      ],
    },
    {
      name: "explain-merge-sort",
      outcome: "pass",
      score: 1.0,
      weightedScore: 1.0,
      duration: 8,
      graderResults: [
        {
          name: "output-exists",
          type: "code",
          passed: true,
          score: 1.0,
          weight: 1.0,
          message: "Output file found",
        },
      ],
    },
  ],
};

export const RUN_DETAIL_B = {
  ...RUNS[1],
  tasks: [
    {
      name: "explain-fibonacci",
      outcome: "pass",
      score: 1.0,
      weightedScore: 1.0,
      duration: 15,
      graderResults: [
        {
          name: "output-exists",
          type: "code",
          passed: true,
          score: 1.0,
          weight: 1.0,
          message: "Output file found",
        },
      ],
      transcript: [
        { type: "Turn", content: "Let me examine the fibonacci implementation." },
        { type: "ToolExecutionStart", toolCallId: "tc-b1", toolName: "read_file", arguments: { path: "fibonacci.py" } },
        { type: "ToolExecutionComplete", toolCallId: "tc-b1", toolResult: "def fib(n): return n if n < 2 else fib(n-1) + fib(n-2)", success: true },
        { type: "ToolExecutionStart", toolCallId: "tc-b2", toolName: "run_tests", arguments: { suite: "fib_tests" } },
        { type: "ToolExecutionComplete", toolCallId: "tc-b2", toolResult: "All tests passed", success: true },
        { type: "ToolExecutionStart", toolCallId: "tc-b3", toolName: "write_file", arguments: { path: "explanation.md", content: "# Fibonacci Analysis" } },
        { type: "ToolExecutionComplete", toolCallId: "tc-b3", toolResult: "File written", success: true },
      ],
      sessionDigest: {
        totalTurns: 4,
        toolCallCount: 3,
        tokensIn: 5200,
        tokensOut: 2800,
        tokensTotal: 8000,
        toolsUsed: ["read_file", "run_tests", "write_file"],
        errors: [],
      },
    },
    {
      name: "explain-quicksort",
      outcome: "fail",
      score: 0.0,
      weightedScore: 0.0,
      duration: 20,
      graderResults: [
        {
          name: "output-exists",
          type: "code",
          passed: false,
          score: 0.0,
          weight: 1.0,
          message: "Output file not found",
        },
      ],
    },
  ],
};

export const HEALTH = { status: "ok", version: "0.1.0" };
