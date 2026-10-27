import type { RunDetail, RunSummary } from "../../src/api/client";
import fixture from "./conversation.json" with { type: "json" };

// API projection checked against the saved artifact by TestConversationSavedResultThroughHTTP.
export const CONVERSATION_SUMMARY: RunSummary = {
  id: "conversation",
  spec: "conversation-review",
  skill: "conversation-review",
  model: "mock-model",
  timestamp: "2026-10-27T12:00:00Z",
  repetitions: 2,
  source: "local",
  taskCount: 2,
  passCount: 2,
  outcome: "passed",
  tokens: 0,
  premiumRequests: 0,
  cost: 0,
  costSource: "estimate",
  duration: 0,
};

export const CONVERSATION_DETAIL: RunDetail = {
  ...CONVERSATION_SUMMARY,
  tasks: [{
    id: "multi-turn",
    name: "Review a multi-turn exchange",
    outcome: "passed",
    score: 0,
    duration: 0,
    graderResults: [],
    transcript: fixture.expectedTranscript,
    responder: { followupsSent: 2, outcome: "stopped" },
  }, {
    id: "empty-messages",
    name: "Messages without text",
    outcome: "passed",
    score: 0,
    duration: 0,
    graderResults: [],
    transcript: fixture.expectedEmptyTranscript,
  }],
};
