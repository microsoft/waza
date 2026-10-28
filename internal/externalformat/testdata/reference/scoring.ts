/**
 * Scoring primitives for the evaluation engine.
 *
 * Scoring model:
 *   score  ∈ [0, 1]  — continuous quality signal
 *   verdict           — binary classification derived from score via threshold
 *
 *   score >= threshold  →  'pass'
 *   score <  threshold  →  'fail'
 *   (infrastructure skip)    →  'skip'
 *
 * Scoring scale principle:
 *   All user-configurable score thresholds use 0-1 scale.
 *   The only 0-10 values in YAML are `score_ranges` which define LLM integer output band labels.
 *
 * Default threshold is 0.8. Override via CLI `--threshold`, suite `execution.threshold`,
 * or per-test `execution.threshold`. All verdict derivation flows through scoreToVerdict().
 */

import type { EvaluationVerdict } from '../types.js';
import type { EvaluationScore } from './types.js';

/** Default score threshold for pass verdict (0-1). Scores below this are fail. */
export const DEFAULT_THRESHOLD = 0.8;

export function scoreToVerdict(score: number, threshold = DEFAULT_THRESHOLD): EvaluationVerdict {
  return score >= threshold ? 'pass' : 'fail';
}

export function clampScore(value: number): number {
  if (Number.isNaN(value) || !Number.isFinite(value)) {
    return 0;
  }
  if (value < 0) {
    return 0;
  }
  if (value > 1) {
    return 1;
  }
  return value;
}

export function extractJsonBlob(text: string): string | undefined {
  const match = text.match(/\{[\s\S]*\}/);
  return match?.[0];
}

function repairSchemaNearBooleanFields(text: string): string {
  return text.replace(
    /("passed"\s*:\s*)(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_-]*))/gi,
    (_match, prefix: string, quotedValue?: string, bareValue?: string) => {
      const value = (quotedValue ?? bareValue ?? '').trim().toLowerCase();
      if (value === 'true') {
        return `${prefix}true`;
      }
      if (value === 'false') {
        return `${prefix}false`;
      }
      return `${prefix}false`;
    },
  );
}

export function parseJsonFromText(text: string): unknown {
  const cleaned = typeof text === 'string' ? text.replace(/```json\n?|```/g, '').trim() : '';
  const blob = extractJsonBlob(cleaned) ?? cleaned;
  const repaired = repairSchemaNearBooleanFields(blob);
  return JSON.parse(repaired);
}

export function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

export function parseJsonSafe(payload: string): Record<string, unknown> | undefined {
  try {
    return JSON.parse(payload) as Record<string, unknown>;
  } catch {
    return undefined;
  }
}

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a === null || b === null) return a === b;
  if (typeof a !== typeof b) return false;
  if (typeof a !== 'object') return a === b;

  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    return a.every((val, i) => deepEqual(val, b[i]));
  }

  const aObj = a as Record<string, unknown>;
  const bObj = b as Record<string, unknown>;
  const aKeys = Object.keys(aObj);
  const bKeys = Object.keys(bObj);
  if (aKeys.length !== bKeys.length) return false;
  return aKeys.every((key) => Object.hasOwn(bObj, key) && deepEqual(aObj[key], bObj[key]));
}

/** Verdict inversion map: pass↔fail, skip stays skip. */
const NEGATED_VERDICT: Record<EvaluationVerdict, EvaluationVerdict> = {
  pass: 'fail',
  fail: 'pass',
  skip: 'skip',
};

/**
 * Negate an evaluation score: inverts score (1 - score), swaps pass/fail verdict,
 * and flips passed on each assertion.
 */
export function negateScore(score: EvaluationScore): EvaluationScore {
  return {
    ...score,
    score: clampScore(1 - score.score),
    verdict: NEGATED_VERDICT[score.verdict],
    assertions: score.assertions.map((a) => ({
      ...a,
      passed: !a.passed,
      evidence: a.evidence ? `[Negated] ${a.evidence}` : undefined,
    })),
  };
}
