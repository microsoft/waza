/**
 * Deterministic assertion evaluators.
 *
 * Pure functions that check agent output against simple conditions
 * and return a binary score (0 or 1) with descriptive assertions.
 */

export type AssertionResult = {
  score: number;
  assertions: { text: string; passed: boolean }[];
};

/** Checks if `output` contains the given `value` substring. */
export function runContainsAssertion(output: string, value: string): AssertionResult {
  const passed = output.includes(value);
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed ? `Output contains "${value}"` : `Output does not contain "${value}"`,
        passed,
      },
    ],
  };
}

/** Checks if `output` contains ANY of the given `values`. */
export function runContainsAnyAssertion(
  output: string,
  values: readonly string[],
): AssertionResult {
  const matched = values.filter((v) => output.includes(v));
  const passed = matched.length > 0;
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed
          ? `Output contains "${matched[0]}"`
          : `Output does not contain any of: ${values.map((v) => `"${v}"`).join(', ')}`,
        passed,
      },
    ],
  };
}

/** Checks if `output` contains ALL of the given `values`. */
export function runContainsAllAssertion(
  output: string,
  values: readonly string[],
): AssertionResult {
  const missing = values.filter((v) => !output.includes(v));
  const passed = missing.length === 0;
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed
          ? `Output contains all ${values.length} expected strings`
          : `Output missing: ${missing.map((v) => `"${v}"`).join(', ')}`,
        passed,
      },
    ],
  };
}

/** Case-insensitive check if `output` contains `value`. */
export function runIcontainsAssertion(output: string, value: string): AssertionResult {
  const passed = output.toLowerCase().includes(value.toLowerCase());
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed
          ? `Output contains "${value}" (case-insensitive)`
          : `Output does not contain "${value}" (case-insensitive)`,
        passed,
      },
    ],
  };
}

/** Case-insensitive check if `output` contains ANY of the given `values`. */
export function runIcontainsAnyAssertion(
  output: string,
  values: readonly string[],
): AssertionResult {
  const lower = output.toLowerCase();
  const matched = values.filter((v) => lower.includes(v.toLowerCase()));
  const passed = matched.length > 0;
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed
          ? `Output contains "${matched[0]}" (case-insensitive)`
          : `Output does not contain any of: ${values.map((v) => `"${v}"`).join(', ')} (case-insensitive)`,
        passed,
      },
    ],
  };
}

/** Case-insensitive check if `output` contains ALL of the given `values`. */
export function runIcontainsAllAssertion(
  output: string,
  values: readonly string[],
): AssertionResult {
  const lower = output.toLowerCase();
  const missing = values.filter((v) => !lower.includes(v.toLowerCase()));
  const passed = missing.length === 0;
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed
          ? `Output contains all ${values.length} expected strings (case-insensitive)`
          : `Output missing (case-insensitive): ${missing.map((v) => `"${v}"`).join(', ')}`,
        passed,
      },
    ],
  };
}

/** Checks if `output` starts with `value` (both trimmed). */
export function runStartsWithAssertion(output: string, value: string): AssertionResult {
  const passed = output.trim().startsWith(value.trim());
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed ? `Output starts with "${value}"` : `Output does not start with "${value}"`,
        passed,
      },
    ],
  };
}

/** Checks if `output` ends with `value` (both trimmed). */
export function runEndsWithAssertion(output: string, value: string): AssertionResult {
  const passed = output.trim().endsWith(value.trim());
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed ? `Output ends with "${value}"` : `Output does not end with "${value}"`,
        passed,
      },
    ],
  };
}

/** Checks if `output` matches the given regex `pattern` with optional `flags`. */
export function runRegexAssertion(
  output: string,
  pattern: string,
  flags?: string,
): AssertionResult {
  const regex = new RegExp(pattern, flags);
  const passed = regex.test(output);
  const flagsLabel = flags ? ` (flags: ${flags})` : '';
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed
          ? `Output matches pattern /${pattern}/${flags ?? ''}${flagsLabel}`
          : `Output does not match pattern /${pattern}/${flags ?? ''}${flagsLabel}`,
        passed,
      },
    ],
  };
}

/** Checks if `output` is valid JSON. */
export function runIsJsonAssertion(output: string): AssertionResult {
  let passed = false;
  try {
    JSON.parse(output);
    passed = true;
  } catch {
    // not valid JSON
  }
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed ? 'Output is valid JSON' : 'Output is not valid JSON',
        passed,
      },
    ],
  };
}

/** Checks if `output` exactly equals `value` (both trimmed). */
export function runEqualsAssertion(output: string, value: string): AssertionResult {
  const passed = output.trim() === value.trim();
  return {
    score: passed ? 1 : 0,
    assertions: [
      {
        text: passed ? `Output equals "${value}"` : `Output does not equal "${value}"`,
        passed,
      },
    ],
  };
}
