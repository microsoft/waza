import type { RunDetail } from "../../src/api/client";
import { RUN_DETAIL } from "./mock-data";

export const RECORDED_RELIABILITY: RunDetail = {
  ...RUN_DETAIL,
  outcome: "failed",
  passCount: 1,
  taskCount: 2,
  statistics: {
    successRateCI: { lower: 0.09453120573423074, upper: 0.9054687942657693, mean: 0.5, confidenceLevel: 0.95 },
    passHatK: [5 / 6, 2 / 3, 0.5],
  },
  tasks: [
    {
      name: "mixed-trials", outcome: "failed", score: 0.85, weightedScore: 0.85, duration: 1,
      graderResults: [],
      bootstrapCI: { lower: 0.72, upper: 0.95, mean: 0.85, confidenceLevel: 0.95 },
      passRateCI: { lower: 0.20765960080204765, upper: 0.9385080552796037, mean: 2 / 3, confidenceLevel: 0.95 },
      trials: { passed: 2, total: 3 },
    },
    {
      name: "all-trials-passed", outcome: "passed", score: 1, duration: 1,
      graderResults: [],
      passRateCI: { lower: 0.43850296824495455, upper: 1, mean: 1, confidenceLevel: 0.95 },
      trials: { passed: 3, total: 3 },
    },
  ],
};
