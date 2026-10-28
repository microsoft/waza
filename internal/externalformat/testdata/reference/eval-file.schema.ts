/**
 * Zod schema for eval YAML file format.
 * Used to generate eval.schema.json for AI agent reference.
 *
 * IMPORTANT: This schema describes the YAML input format, not the parsed runtime types.
 * When adding new eval features, update this schema AND run
 * `bun run validate:eval-schema` from the repo root. That command regenerates
 * eval.schema.json and fails if the generated artifact needs to be committed.
 */
import { z } from 'zod/v3';

// ---------------------------------------------------------------------------
// Shared primitives
// ---------------------------------------------------------------------------

const JsonObjectSchema = z.object({}).catchall(z.unknown());
const JsonRecordSchema = z.record(z.unknown());
const UnsupportedPromptfooAssertionTypes = new Set([
  'tool-call-f1',
  'trace-error-spans',
  'trace-span-count',
  'trace-span-duration',
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function formatJsonValue(value: unknown): string {
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function hasToolTrajectoryLatencyCheck(value: Record<string, unknown>): boolean {
  const expected = value.expected;
  return (
    Array.isArray(expected) &&
    expected.some(
      (item) =>
        isRecord(item) && (item.max_duration_ms !== undefined || item.maxDurationMs !== undefined),
    )
  );
}

function staleSkillTriggerMessage(value: Record<string, unknown>): string {
  const skill = typeof value.skill === 'string' && value.skill.trim() ? value.skill.trim() : '';
  const shouldTrigger = value.should_trigger !== false;
  if (!skill) {
    return "Authored assertion type 'skill-trigger' has been removed. Use 'skill-used' with value: <skill> for expected skill use, or 'not-skill-used' with value: <skill> when the skill must not be used.";
  }
  const replacementType = shouldTrigger ? 'skill-used' : 'not-skill-used';
  return `Authored assertion type 'skill-trigger' has been removed. Replace skill: ${skill} with type: ${replacementType}, value: ${skill}.`;
}

function staleToolTrajectoryMessage(value: Record<string, unknown>): string {
  const mode = typeof value.mode === 'string' ? value.mode : undefined;
  if (hasToolTrajectoryLatencyCheck(value)) {
    return "Authored assertion type 'tool-trajectory' has been removed. Per-tool latency checks such as max_duration_ms have no Promptfoo trajectory:* equivalent in AgentV yet; use a custom script assertion or track this as unsupported future scope.";
  }

  if (mode === 'any_order' && isRecord(value.minimums)) {
    const [toolName, min] = Object.entries(value.minimums)[0] ?? [];
    const example =
      typeof toolName === 'string'
        ? ` For example: type: trajectory:tool-used, value: { name: ${toolName}, min: ${formatJsonValue(min)} }.`
        : '';
    return `Authored assertion type 'tool-trajectory' has been removed. Replace mode: any_order minimums with one Promptfoo trajectory:tool-used assertion per tool.${example}`;
  }

  if ((mode === 'in_order' || mode === 'exact') && Array.isArray(value.expected)) {
    const steps = value.expected
      .filter(isRecord)
      .map((item) => item.tool)
      .filter((tool): tool is string => typeof tool === 'string');
    const hasArgs = value.expected.some(
      (item) => isRecord(item) && item.args !== undefined && item.args !== 'any',
    );
    const sequenceHint =
      steps.length > 0
        ? ` Use type: trajectory:tool-sequence, value: { mode: ${mode}, steps: ${formatJsonValue(steps)} }.`
        : ' Use type: trajectory:tool-sequence with value: { mode, steps }.';
    const argsHint = hasArgs
      ? ' Move expected args checks to trajectory:tool-args-match with Promptfoo mode: partial or exact.'
      : '';
    return `Authored assertion type 'tool-trajectory' has been removed.${sequenceHint}${argsHint}`;
  }

  return "Authored assertion type 'tool-trajectory' has been removed. Use Promptfoo trajectory:* assertions: trajectory:tool-used for tool presence/counts, trajectory:tool-sequence for in_order/exact steps, and trajectory:tool-args-match for argument checks. AgentV-specific latency checks are unsupported future scope.";
}

function staleAuthoredAssertionMessage(value: Record<string, unknown>): string | undefined {
  if (typeof value.type !== 'string') return undefined;
  const type = value.type.replace(/_/g, '-');
  if (type === 'llm-grader' || type === 'llm-judge') {
    return "Authored assertion type 'llm-grader' has been removed. Use 'llm-rubric' for free-form rubric checks or 'agent-rubric' for agentic rubric checks.";
  }
  if (type === 'skill-trigger') return staleSkillTriggerMessage(value);
  if (type === 'tool-trajectory') return staleToolTrajectoryMessage(value);
  return undefined;
}

/** Message content: string, structured object, or structured array */
const ContentItemSchema = z.object({
  type: z.enum(['text', 'file', 'image']),
  value: z.string(),
});

const MessageContentSchema = z.union([z.string(), JsonObjectSchema, z.array(ContentItemSchema)]);

const MessageSchema = z.object({
  role: z.enum(['system', 'user', 'assistant', 'tool']),
  content: MessageContentSchema,
});

const InputObjectShorthandSchema = z.object({ role: z.never().optional() }).catchall(z.unknown());

/** Input: string/object shorthand, single message, or message array */
const InputSchema = z.union([
  z.string(),
  MessageSchema,
  InputObjectShorthandSchema,
  z.array(MessageSchema),
]);

/** Expected output: string, object, or message array */
const ExpectedOutputSchema = z.union([z.string(), JsonObjectSchema, z.array(MessageSchema)]);

// ---------------------------------------------------------------------------
// Grader schemas (YAML input format)
// ---------------------------------------------------------------------------

/** Common fields shared by all evaluators */
const EvaluatorCommonSchema = z.object({
  metric: z.string().optional(),
  weight: z.number().min(0).optional(),
  required: z.boolean().optional(),
  /** Minimum score (0-1) for this evaluator to pass. Independent of `required` gate. */
  min_score: z.number().gt(0).lte(1).optional(),
  negate: z.boolean().optional(),
});

/** Prompt: string (inline/file path), promptfoo-shaped object, or executable script config */
const PromptSchema = z.union([
  z.string(),
  z.object({
    command: z.union([z.string(), z.array(z.string())]),
    config: z.record(z.unknown()).optional(),
  }),
  z
    .object({
      id: z.string().optional(),
      label: z.string().optional(),
      raw: z.string().optional(),
      function: z.string().optional(),
      function_file: z.string().optional(),
      path: z.string().optional(),
      prefix: z.string().optional(),
      suffix: z.string().optional(),
      config: JsonRecordSchema.optional(),
    })
    .passthrough(),
]);

const PromptsSchema = z.union([PromptSchema, z.array(PromptSchema).min(1)]);

/** Score range for analytic rubrics */
const ScoreRangeSchema = z.object({
  score_range: z.tuple([z.number().int().min(0).max(10), z.number().int().min(0).max(10)]),
  outcome: z.string().min(1),
});

/** Rubric item (checklist or score-range mode) */
const RubricItemSchema = z.object({
  id: z.string().optional(),
  outcome: z.string().optional(),
  operator: z.enum(['correctness', 'contradiction']).optional(),
  weight: z.number().optional(),
  required: z.boolean().optional(),
  /** Minimum score (0-1) for this criterion to pass. */
  min_score: z.number().gt(0).lte(1).optional(),
  score_ranges: z.array(ScoreRangeSchema).optional(),
});

// --- Type-specific evaluator schemas ---

const ScriptGraderSchema = EvaluatorCommonSchema.extend({
  type: z.literal('script'),
  command: z.union([z.string(), z.array(z.string())]),
  cwd: z.string().optional(),
  provider: z.union([z.boolean(), z.object({ max_calls: z.number().optional() })]).optional(),
  config: z.record(z.unknown()).optional(),
});

const IncludeSchema = z
  .object({
    include: z.string().min(1),
  })
  .strict();

const ArgsMatchSchema = z.union([
  z.enum(['exact', 'ignore', 'subset', 'superset']),
  z.array(z.string()),
]);

const ToolTrajectoryExpectedItemSchema = z.object({
  tool: z.string(),
  args: z.union([z.literal('any'), z.record(z.unknown())]).optional(),
  max_duration_ms: z.number().min(0).optional(),
  maxDurationMs: z.number().min(0).optional(),
  args_match: ArgsMatchSchema.optional(),
  argsMatch: ArgsMatchSchema.optional(),
});

const ToolTrajectorySchema = EvaluatorCommonSchema.extend({
  type: z.literal('tool-trajectory'),
  mode: z.enum(['any_order', 'in_order', 'exact', 'subset', 'superset']),
  minimums: z.record(z.number().int().min(0)).optional(),
  expected: z.array(ToolTrajectoryExpectedItemSchema).optional(),
  args_match: ArgsMatchSchema.optional(),
  argsMatch: ArgsMatchSchema.optional(),
});

const TrajectoryAssertionSchema = EvaluatorCommonSchema.extend({
  type: z.enum([
    'trajectory:tool-used',
    'trajectory:tool-args-match',
    'trajectory:tool-sequence',
    'trajectory:step-count',
    'trajectory:goal-success',
  ]),
  value: z.unknown().optional(),
});

const FieldConfigSchema = z.object({
  path: z.string(),
  match: z.enum(['exact', 'numeric_tolerance', 'date']),
  required: z.boolean().optional(),
  weight: z.number().optional(),
  tolerance: z.number().min(0).optional(),
  relative: z.boolean().optional(),
  formats: z.array(z.string()).optional(),
});

const FieldAccuracySchema = EvaluatorCommonSchema.extend({
  type: z.literal('field-accuracy'),
  fields: z.array(FieldConfigSchema).min(1),
  aggregation: z.enum(['weighted_average', 'all_or_nothing']).optional(),
});

const LatencySchema = EvaluatorCommonSchema.extend({
  type: z.literal('latency'),
  threshold: z.number().min(0),
});

const CostSchema = EvaluatorCommonSchema.extend({
  type: z.literal('cost'),
  budget: z.number().min(0),
});

const TokenUsageSchema = EvaluatorCommonSchema.extend({
  type: z.literal('token-usage'),
  max_total: z.number().min(0).optional(),
  max_input: z.number().min(0).optional(),
  max_output: z.number().min(0).optional(),
});

const ExecutionMetricsSchema = EvaluatorCommonSchema.extend({
  type: z.literal('execution-metrics'),
  max_tool_calls: z.number().min(0).optional(),
  max_llm_calls: z.number().min(0).optional(),
  max_tokens: z.number().min(0).optional(),
  max_cost_usd: z.number().min(0).optional(),
  max_duration_ms: z.number().min(0).optional(),
  target_exploration_ratio: z.number().min(0).max(1).optional(),
  exploration_tolerance: z.number().min(0).optional(),
});

const ContainsSchema = EvaluatorCommonSchema.extend({
  type: z.literal('contains'),
  value: z.string(),
});

const RegexSchema = EvaluatorCommonSchema.extend({
  type: z.literal('regex'),
  value: z.string(),
});

const IsJsonSchema = EvaluatorCommonSchema.extend({
  type: z.literal('is-json'),
});

const EqualsSchema = EvaluatorCommonSchema.extend({
  type: z.literal('equals'),
  value: z.string(),
});

const PromptfooAssertionSchema = EvaluatorCommonSchema.extend({
  type: z.enum([
    'assert-set',
    'llm-rubric',
    'agent-rubric',
    'javascript',
    'python',
    'webhook',
    'similar',
    'skill-used',
    'not-skill-used',
    'contains',
    'contains-any',
    'contains-all',
    'icontains',
    'icontains-any',
    'icontains-all',
    'starts-with',
    'ends-with',
    'regex',
    'is-json',
    'equals',
  ]),
  value: z.unknown().optional(),
  threshold: z.number().min(0).max(1).optional(),
  provider: z.union([z.string(), JsonObjectSchema]).optional(),
  config: JsonRecordSchema.optional(),
  assert: z.array(z.union([z.string(), JsonObjectSchema])).optional(),
  transform: z.union([z.string(), JsonObjectSchema]).optional(),
}).passthrough();

/** Union of all grader types */
const EvaluatorSchema = z.union([
  ScriptGraderSchema,
  PromptfooAssertionSchema,
  IncludeSchema,
  ToolTrajectorySchema,
  TrajectoryAssertionSchema,
  FieldAccuracySchema,
  LatencySchema,
  CostSchema,
  TokenUsageSchema,
  ExecutionMetricsSchema,
  ContainsSchema,
  RegexSchema,
  IsJsonSchema,
  EqualsSchema,
]);

const AssertionObjectSchema = JsonObjectSchema.superRefine((value, ctx) => {
  if (value.preprocessors !== undefined) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['preprocessors'],
      message: 'preprocessors has been removed from authored eval YAML. Use transform instead.',
    });
  }
  if (value.postprocess !== undefined) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['postprocess'],
      message: 'postprocess has been removed. Use transform instead.',
    });
  }
  if (value.type === 'script' && value.target !== undefined) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['target'],
      message:
        "Script evaluator field 'target' has been removed. Use script evaluator 'provider' instead.",
    });
  } else if (typeof value.target === 'string') {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['target'],
      message: "Assertion field 'target' has been removed. Use assertion 'provider' instead.",
    });
  }
  const rawType = value.type;
  if (typeof rawType !== 'string') {
    return;
  }
  const type = rawType.replace(/_/g, '-');
  const staleMessage = staleAuthoredAssertionMessage(value);
  if (staleMessage) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['type'],
      message: staleMessage,
    });
    return;
  }
  if (type === 'composite') {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['type'],
      message: "Unsupported assertion type 'composite'. Use 'assert-set' instead.",
    });
    return;
  }
  const baseType = type.startsWith('not-') ? type.slice(4) : type;
  if (UnsupportedPromptfooAssertionTypes.has(baseType)) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['type'],
      message: `Unsupported promptfoo assertion type '${rawType}'. This type is future scope in AgentV and is not accepted as a custom assertion.`,
    });
  }
});

/** Assertion item: string shorthand (becomes a criteria/rubric grader) or full evaluator config. */
const AssertionItemSchema = z.union([z.string(), AssertionObjectSchema]);

// ---------------------------------------------------------------------------
// Workspace
// ---------------------------------------------------------------------------

const WorkspaceScriptSchema = z
  .object({
    command: z.union([z.string(), z.array(z.string())]).optional(),
    timeout_ms: z.number().min(0).optional(),
    cwd: z.string().optional(),
  })
  .strict();

const ExtensionHookSchema = z.enum(['beforeAll', 'beforeEach', 'afterEach', 'afterAll']);

const FileExtensionSchema = z
  .string()
  .min(1)
  .refine((value) => value.startsWith('file://'), {
    message: 'file extensions must start with file://',
  })
  .refine(
    (value) => {
      const lastColon = value.lastIndexOf(':');
      return (
        lastColon > 'file://'.length &&
        ExtensionHookSchema.safeParse(value.slice(lastColon + 1)).success
      );
    },
    {
      message: 'file extensions must be of the form file://path/to/hook.ts:beforeAll',
    },
  );

const AgentRulesStringExtensionSchema = z.union([
  z.literal('agentv:agent-rules'),
  z
    .string()
    .startsWith('agentv:agent-rules:')
    .refine(
      (value) => ExtensionHookSchema.safeParse(value.slice('agentv:agent-rules:'.length)).success,
      {
        message: 'agentv:agent-rules hook must be beforeAll, beforeEach, afterEach, or afterAll',
      },
    ),
]);

const AgentRulesPathListSchema = z.union([z.string().min(1), z.array(z.string().min(1))]);

const AgentRulesObjectExtensionSchema = z
  .object({
    id: z.literal('agentv:agent-rules'),
    hook: ExtensionHookSchema.optional(),
    skills: AgentRulesPathListSchema.optional(),
    hooks: AgentRulesPathListSchema.optional(),
    agents: AgentRulesPathListSchema.optional(),
    rules: AgentRulesPathListSchema.optional(),
    config: z
      .object({
        skills: AgentRulesPathListSchema.optional(),
        hooks: AgentRulesPathListSchema.optional(),
        agents: AgentRulesPathListSchema.optional(),
        rules: AgentRulesPathListSchema.optional(),
      })
      .strict()
      .optional(),
  })
  .strict();

const ExtensionSchema = z.union([
  FileExtensionSchema,
  AgentRulesStringExtensionSchema,
  AgentRulesObjectExtensionSchema,
]);

// ---------------------------------------------------------------------------
// Lifecycle hooks
// ---------------------------------------------------------------------------

const LifecycleHookSchema = z
  .object({
    command: z.union([z.string(), z.array(z.string())]).optional(),
    timeout_ms: z.number().optional(),
    timeoutMs: z.number().optional(),
    cwd: z.string().optional(),
    reset: z.enum(['none', 'fast', 'strict']).optional(),
  })
  .strict();

const LifecycleHooksSchema = z
  .object({
    enabled: z.boolean().optional(),
    before_all: LifecycleHookSchema.optional(),
    before_each: LifecycleHookSchema.optional(),
    after_each: LifecycleHookSchema.optional(),
    after_all: LifecycleHookSchema.optional(),
  })
  .strict();

const EnvironmentSetupBaseSchema = z
  .object({
    command: z
      .array(z.string().min(1), {
        invalid_type_error:
          'environment.setup.command must be a non-empty argv array. Use ["bash", "-lc", "..."] for shell behavior.',
      })
      .min(1, {
        message:
          'environment.setup.command must be a non-empty argv array. Use ["bash", "-lc", "..."] for shell behavior.',
      }),
    cwd: z.string().min(1).optional(),
    timeout_ms: z.number().gt(0).optional(),
  })
  .strict();

const EnvironmentSetupSchema = z
  .unknown()
  .superRefine((value, ctx) => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      return;
    }
    const record = value as Record<string, unknown>;
    if (Object.prototype.hasOwnProperty.call(record, 'args')) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['args'],
        message:
          'environment.setup.args is not supported. Put executable arguments in environment.setup.command as argv entries.',
      });
    }
    if (Object.prototype.hasOwnProperty.call(record, 'env')) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['env'],
        message:
          'environment.setup.env is not supported. Use environment.env for environment-scoped variables.',
      });
    }
    if (Object.prototype.hasOwnProperty.call(record, 'timeout_seconds')) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['timeout_seconds'],
        message:
          'environment.setup.timeout_seconds is not supported. Use timeout_ms in milliseconds.',
      });
    }
    for (const key of Object.keys(record)) {
      if (!['command', 'cwd', 'timeout_ms', 'args', 'env', 'timeout_seconds'].includes(key)) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: [key],
          message: `environment.setup.${key} is not supported. Supported fields are command, cwd, and timeout_ms.`,
        });
      }
    }
  })
  .pipe(EnvironmentSetupBaseSchema);

const EnvironmentBaseSchema = z.object({
  workdir: z.string().min(1),
  setup: EnvironmentSetupSchema.optional(),
  env: z.record(z.string()).optional(),
});

const HostEnvironmentSchema = EnvironmentBaseSchema.extend({
  type: z.literal('host'),
}).strict();

const DockerEnvironmentMountSchema = z
  .object({
    source: z.string().min(1),
    target: z.string().min(1),
    access: z.enum(['ro', 'rw']).optional(),
    read_only: z.boolean().optional(),
  })
  .strict();

const DockerEnvironmentResourcesSchema = z
  .object({
    cpus: z.number().gt(0).optional(),
    memory: z.string().min(1).optional(),
    disk: z.string().min(1).optional(),
    gpu: z.union([z.boolean(), z.string().min(1)]).optional(),
  })
  .strict();

const DockerEnvironmentSchema = EnvironmentBaseSchema.extend({
  type: z.literal('docker'),
  context: z.string().min(1).optional(),
  dockerfile: z.string().min(1).optional(),
  image: z.string().min(1).optional(),
  resources: DockerEnvironmentResourcesSchema.optional(),
  mounts: z.array(DockerEnvironmentMountSchema).optional(),
  secrets: z.record(z.string()).optional(),
})
  .strict()
  .refine((value) => value.context !== undefined || value.image !== undefined, {
    message: "Docker environment recipes must define either 'image' or 'context'.",
  });

const EnvironmentSchema = z.union([
  z.string().regex(/^\s*file:\/\//, 'environment string must start with file://'),
  HostEnvironmentSchema,
  DockerEnvironmentSchema,
]);

// ---------------------------------------------------------------------------
// Target hooks (eval-level per-target customization)
// ---------------------------------------------------------------------------

const TargetHooksSchema = z
  .object({
    before_all: LifecycleHookSchema.optional(),
    before_each: LifecycleHookSchema.optional(),
    after_each: LifecycleHookSchema.optional(),
    after_all: LifecycleHookSchema.optional(),
  })
  .strict();

/** Eval target reference: string shorthand or object with hooks */
const EvalTargetRefSchema = z
  .object({
    id: z.string().min(1),
    use_target: z.string().optional(),
    hooks: TargetHooksSchema.optional(),
  })
  .strict();

const EvalLocalTargetSchema = z
  .object({
    id: z.string().min(1).optional(),
    extends: z.string().min(1).optional(),
    provider: z.string().min(1).optional(),
    model: z.string().min(1).optional(),
    config: JsonRecordSchema.optional(),
    prompts: PromptsSchema.optional(),
    transform: z.union([z.string(), JsonObjectSchema]).optional(),
    delay: z.number().min(0).optional(),
    env: z.record(z.string()).optional(),
    environment: z.never().optional(),
    container: z.never().optional(),
    install: z.never().optional(),
    reasoning_effort: z.string().min(1).optional(),
    hooks: TargetHooksSchema.optional(),
  })
  .passthrough();

const EvalTargetSchema = z.union([z.string().min(1), EvalLocalTargetSchema]);
const EvalTargetsSchema = z.union([EvalTargetSchema, z.array(EvalTargetSchema).min(1)]);
const EvalProviderExtensionFieldsSchema = z.object({
  runtime: z
    .union([
      z.enum(['host', 'profile', 'sandbox']),
      z
        .object({
          mode: z.enum(['host', 'profile', 'sandbox']),
        })
        .passthrough(),
    ])
    .optional(),
  environment: EnvironmentSchema.optional(),
  hooks: TargetHooksSchema.optional(),
});
const EvalProviderOptionsSchema = z
  .object({
    label: z.string().min(1).optional(),
    config: JsonRecordSchema.optional(),
    prompts: PromptsSchema.optional(),
    transform: z.union([z.string(), JsonObjectSchema]).optional(),
    delay: z.number().min(0).optional(),
    env: z.record(z.string()).optional(),
    inputs: JsonRecordSchema.optional(),
  })
  .merge(EvalProviderExtensionFieldsSchema)
  .strict();
const EvalProviderObjectSchema = EvalProviderOptionsSchema.extend({
  id: z.string().min(1),
  provider: z
    .never({
      invalid_type_error:
        'providers[].provider has been removed. Use providers[].id for the backend and providers[].label for the stable identity.',
    })
    .optional(),
  name: z
    .never({
      invalid_type_error: 'providers[].name has been removed. Use providers[].label.',
    })
    .optional(),
  container: z.never().optional(),
  install: z.never().optional(),
}).strict();
const EvalProviderMapOptionsSchema = EvalProviderOptionsSchema.extend({
  id: z.string().min(1).optional(),
}).strict();
const EvalProviderMapSchema = z
  .record(z.string().min(1), EvalProviderMapOptionsSchema)
  .refine(
    (value) =>
      !Object.prototype.hasOwnProperty.call(value, 'id') &&
      !Object.prototype.hasOwnProperty.call(value, 'label') &&
      !Object.prototype.hasOwnProperty.call(value, 'config'),
    {
      message:
        'Provider maps must be keyed by provider id, e.g. { "openai:gpt-4": { label, config } }.',
    },
  );
const EvalProviderSchema = z.union([
  z.string().min(1),
  EvalProviderObjectSchema,
  EvalProviderMapSchema,
]);
const EvalProvidersSchema = z.union([EvalProviderSchema, z.array(EvalProviderSchema).min(1)]);

// ---------------------------------------------------------------------------
// Execution block
// ---------------------------------------------------------------------------

/** Execution error tolerance: true or false */
const FailOnErrorSchema = z.boolean();

const ExecutionSchema = z.object({
  target: z
    .never({
      invalid_type_error: "execution.target has been removed. Use top-level 'providers'.",
    })
    .optional(),
  targets: z
    .never({
      invalid_type_error: "execution.targets has been removed. Use top-level 'providers'.",
    })
    .optional(),
  workers: z.never().optional(),
  assert: z.array(AssertionItemSchema).optional(),
  skip_defaults: z.boolean().optional(),
  cache: z.boolean().optional(),
  /** Removed before stable release. Repeat counts belong under evaluate_options.repeat. */
  trials: z.never().optional(),
  budget_usd: z.number().min(0).optional(),
  budgetUsd: z.number().min(0).optional(),
  fail_on_error: FailOnErrorSchema.optional(),
  failOnError: FailOnErrorSchema.optional(),
  threshold: z.number().min(0).max(1).optional(),
});

const RunOverrideSchema = z
  .object({
    threshold: z.number().min(0).max(1).optional(),
    repeat: z.number().int().min(1).optional(),
    timeout_seconds: z.number().gt(0).optional(),
    budget_usd: z.number().gt(0).optional(),
  })
  .strict();

const DefaultTestSchema = z
  .object({
    vars: JsonObjectSchema.optional(),
    provider: EvalTargetSchema.optional(),
    providers: EvalTargetsSchema.optional(),
    prompts: PromptsSchema.optional(),
    expected_output: z.never().optional(),
    assert: z.array(AssertionItemSchema).optional(),
    assert_scoring_function: z.union([z.string().min(1), JsonObjectSchema]).optional(),
    options: JsonObjectSchema.optional(),
    threshold: z.number().min(0).max(1).optional(),
    metadata: z.record(z.unknown()).optional(),
  })
  .strict();

const DefaultTestReferenceSchema = z
  .string()
  .regex(/^\s*(file|ref):\/\//, 'default_test string must start with file:// or ref://');

const EvaluateOptionsSchema = z
  .object({
    budget_usd: z.number().gt(0).optional(),
    max_concurrency: z.number().int().min(1).max(50).optional(),
    cache: z.union([z.boolean(), JsonObjectSchema]).optional(),
    delay: z.number().min(0).optional(),
    generate_suggestions: z.boolean().optional(),
    repeat: z.number().int().min(1).optional(),
    timeout_ms: z.number().gt(0).optional(),
    max_eval_time_ms: z.number().gt(0).optional(),
    filter_range: z.union([z.tuple([z.number(), z.number()]), z.string()]).optional(),
  })
  .strict();

/** A single turn in a multi-turn conversation */
const ConversationTurnSchema = z.object({
  input: z.union([z.string(), MessageContentSchema]),
  expected_output: z.union([z.string(), MessageContentSchema]).optional(),
  assert: z.array(AssertionItemSchema).optional(),
});

// ---------------------------------------------------------------------------
// Test case
// ---------------------------------------------------------------------------

const TestExecutionSchema = ExecutionSchema.omit({ target: true, targets: true }).strict();

const EvalTestBaseSchema = z
  .object({
    id: z.string().min(1).optional(),
    description: z.string().optional(),
    vars: JsonObjectSchema.optional(),
    provider: EvalTargetSchema.optional(),
    providers: EvalTargetsSchema.optional(),
    prompts: PromptsSchema.optional(),
    input: z.never().optional(),
    input_files: z.array(z.string()).optional(),
    expected_output: z.never().optional(),
    assert: z.array(AssertionItemSchema).optional(),
    assert_scoring_function: z.union([z.string().min(1), JsonObjectSchema]).optional(),
    options: JsonObjectSchema.optional(),
    threshold: z.number().min(0).max(1).optional(),
    execution: TestExecutionSchema.optional(),
    run: RunOverrideSchema.optional(),
    environment: EnvironmentSchema.optional(),
    metadata: z.record(z.unknown()).optional(),
    conversation_id: z.string().optional(),
    suite: z.string().optional(),
    depends_on: z.array(z.string()).optional(),
    on_dependency_failure: z.enum(['skip', 'fail', 'run']).optional(),
    mode: z.enum(['conversation']).optional(),
    turns: z.array(ConversationTurnSchema).min(1).optional(),
    aggregation: z.enum(['mean', 'min', 'max']).optional(),
    on_turn_failure: z.enum(['continue', 'stop']).optional(),
    window_size: z.number().int().min(1).optional(),
  })
  .passthrough();

function rejectProviderOutput(value: Record<string, unknown>, context: z.RefinementCtx): void {
  if (Object.prototype.hasOwnProperty.call(value, 'provider_output')) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['provider_output'],
      message:
        'tests[].provider_output is not supported in authored AgentV YAML. Use an explicit deterministic target such as provider: cli for fixed outputs, or use a replay/fixture target for captured provider responses.',
    });
  }
}

const EvalTestSchema = EvalTestBaseSchema.superRefine(rejectProviderOutput);

const SelectPatternSchema = z.union([z.string().min(1), z.array(z.string().min(1)).min(1)]);
const SelectMetadataValueSchema = z.union([
  z.string(),
  z.number(),
  z.boolean(),
  z.array(z.union([z.string(), z.number(), z.boolean()])).min(1),
]);
const TestIncludeSelectSchema = z
  .object({
    test_ids: SelectPatternSchema.optional(),
    tags: SelectPatternSchema.optional(),
    metadata: z.record(SelectMetadataValueSchema).optional(),
  })
  .strict();

const TestIncludeSchema = z
  .object({
    include: z.string().min(1),
    type: z.enum(['suite', 'tests']),
    select: z.union([SelectPatternSchema, TestIncludeSelectSchema]).optional(),
    run: RunOverrideSchema.optional(),
  })
  .strict();

const TestsSchema = z.union([
  z.array(z.union([EvalTestSchema, TestIncludeSchema, z.string().min(1)])),
  z.string().min(1),
]);

const ConfigRuntimeSchema = z.union([
  z.enum(['host', 'profile', 'sandbox']),
  z
    .object({
      mode: z.enum(['host', 'profile', 'sandbox']),
    })
    .passthrough(),
]);

const ConfigTargetSchema = z
  .object({
    id: z.string().min(1),
    provider: z.string().min(1),
    runtime: ConfigRuntimeSchema,
    config: JsonRecordSchema.optional(),
    environment: z.never().optional(),
    container: z.never().optional(),
    install: z.never().optional(),
  })
  .strict();

const ConfigDefaultsSchema = z
  .object({
    target: z
      .never({
        invalid_type_error: 'defaults.target has been removed. Use defaults.provider.',
      })
      .optional(),
    provider: z.string().min(1).optional(),
    grader: z.string().min(1).optional(),
  })
  .strict();

const ScenarioConfigSchema = EvalTestBaseSchema.partial().superRefine(rejectProviderOutput);

const ScenarioSchema = z
  .object({
    description: z.string().optional(),
    config: z.array(ScenarioConfigSchema),
    tests: z.array(EvalTestSchema),
  })
  .strict();
const ScenarioReferenceSchema = z
  .string()
  .min(1)
  .refine((value) => value.startsWith('file://'), {
    message: 'Scenario references must use file://...',
  });

const DerivedMetricSchema = z
  .object({
    name: z.string().min(1),
    value: z.union([z.string().min(1), JsonObjectSchema]),
  })
  .strict();

const TagsSchema = z.union([
  z.array(z.string()),
  z.record(z.union([z.string(), z.number(), z.boolean()])),
]);

const TOP_LEVEL_IMPORTS_MESSAGE =
  "Top-level 'imports' is not supported. Run eval files directly with CLI multi-file selection and tags for grouping. For raw case files, use tests: file://... or string entries under tests. For reusable scenarios, use scenarios: [file://...]. For reusable config, use prompts: file://..., default_test: file://..., and environment: file://... for coding-agent testbeds.";

const TOP_LEVEL_EXPERIMENT_MESSAGE =
  "Top-level 'experiment' has been removed from authored eval YAML. Use tags.experiment in the eval file or CLI --experiment at run time.";

// ---------------------------------------------------------------------------
// Top-level eval file
// ---------------------------------------------------------------------------

export const EvalFileSchemaInput: z.ZodType = z.object({
  $schema: z.string().optional(),
  // Metadata
  name: z
    .string()
    .regex(/^[a-z0-9-]+$/)
    .optional(),
  description: z.string().optional(),
  category: z.string().optional(),
  version: z.string().optional(),
  author: z.string().optional(),
  tags: TagsSchema.optional(),
  license: z.string().optional(),
  requires: z.object({ agentv: z.string().optional() }).optional(),
  expected_output: z.never().optional(),
  // Suite-level input
  input: z.never().optional(),
  prompts: PromptsSchema.optional(),
  // Suite-level input_files shorthand
  input_files: z.array(z.string()).optional(),
  imports: z.never({ invalid_type_error: TOP_LEVEL_IMPORTS_MESSAGE }).optional(),
  // Tests (inline raw cases, legacy include entries, or external raw-case path)
  tests: TestsSchema.optional(),
  providerPromptMap: z
    .never({
      invalid_type_error: "Top-level 'providerPromptMap' is not supported. Use 'providers'.",
    })
    .optional(),
  provider_prompt_map: z
    .never({
      invalid_type_error: "Top-level 'provider_prompt_map' is not supported. Use 'providers'.",
    })
    .optional(),
  // Shared composable config graph fields
  graders: z
    .never({
      invalid_type_error:
        "Top-level 'graders' has been removed. A grader is just a provider — move each entry into 'providers' and select it via 'defaults.grader' or an assertion's provider override.",
    })
    .optional(),
  defaults: z.union([ConfigDefaultsSchema, z.string().min(1)]).optional(),
  // Removed legacy aliases
  eval_cases: z
    .never({
      invalid_type_error:
        "Top-level 'eval_cases' has been removed from authored eval YAML. Use 'tests' instead.",
    })
    .optional(),
  evalcases: z
    .never({
      invalid_type_error:
        "Top-level 'evalcases' has been removed from authored eval YAML. Use 'tests' instead.",
    })
    .optional(),
  // Target
  target: z
    .never({
      invalid_type_error: "Top-level 'target' has been removed. Use top-level 'providers'.",
    })
    .optional(),
  targets: z
    .never({
      invalid_type_error:
        "Top-level 'targets' has been removed. Use 'providers'; map targets[].id to providers[].label and targets[].provider to providers[].id.",
    })
    .optional(),
  providers: EvalProvidersSchema.optional(),
  model: z.never().optional(),
  // Flat run controls. Group authored YAML with tags.experiment or CLI --experiment.
  experiment: z.never({ invalid_type_error: TOP_LEVEL_EXPERIMENT_MESSAGE }).optional(),
  repeat: z.never().optional(),
  runs: z.never().optional(),
  early_exit: z.never().optional(),
  timeout_seconds: z.number().gt(0).optional(),
  evaluate_options: EvaluateOptionsSchema.optional(),
  budget_usd: z.never().optional(),
  threshold: z.number().min(0).max(1).optional(),
  default_test: z.union([DefaultTestReferenceSchema, DefaultTestSchema]).optional(),
  environment: EnvironmentSchema.optional(),
  scenarios: z.array(z.union([ScenarioSchema, ScenarioReferenceSchema])).optional(),
  derived_metrics: z.array(DerivedMetricSchema).optional(),
  output_path: z.union([z.string().min(1), z.array(z.string().min(1))]).optional(),
  env: z.record(z.string()).optional(),
  nunjucks_filters: z.union([JsonObjectSchema, z.array(z.string().min(1))]).optional(),
  extensions: z.array(ExtensionSchema).optional(),
  on_run_complete: z.never().optional(),
  policy: z.never().optional(),
  execution: z.never().optional(),
  // Suite-level assert entries
  assert: z.array(AssertionItemSchema).optional(),
  preprocessors: z.never().optional(),
});

export const EvalFileSchema: z.ZodType = EvalFileSchemaInput.refine(
  (value) => value.tests !== undefined || value.scenarios !== undefined,
  { message: "Eval files must define 'tests' or 'scenarios'." },
);
