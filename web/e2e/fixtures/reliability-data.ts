import type { RunDetail } from "../../src/api/client";
import { RUN_DETAIL } from "./mock-data";

export const RECORDED_RELIABILITY: RunDetail = {
  ...RUN_DETAIL,
  outcome: "failed",
  passCount: 1,
  taskCount: 2,
  statistics: {
    successRateCI: { lower: 0.0945, upper: 0.9055, mean: 0.5, confidenceLevel: 0.95 },
    passHatK: [5 / 6, 2 / 3, 0.5],
  },
  tasks: [
    {
      name: "mixed-trials", outcome: "failed", score: 0.85, weightedScore: 0.85, duration: 1,
      graderResults: [],
      bootstrapCI: { lower: 0.72, upper: 0.95, mean: 0.85, confidenceLevel: 0.95 },
      passRateCI: { lower: 0.2077, upper: 0.9385, mean: 2 / 3, confidenceLevel: 0.95 },
      trials: { passed: 2, total: 3 },
    },
    {
      name: "all-trials-passed", outcome: "passed", score: 1, duration: 1,
      graderResults: [],
      passRateCI: { lower: 0.4385, upper: 1, mean: 1, confidenceLevel: 0.95 },
      trials: { passed: 3, total: 3 },
    },
  ],
};
