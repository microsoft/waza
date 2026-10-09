import type { EvidenceManifest, EvidenceRun, RunDetail } from "../../src/api/client";
import { RUN_DETAIL } from "./mock-data";

export function evidenceManifest(runNumber = 1): EvidenceManifest {
  return {
    version: "1.0",
    sha256: "a".repeat(64),
    origin: {
      eval_id: "source-evaluation",
      task_id: "report-task",
      run_number: runNumber,
      attempt_count: 2,
      prior_attempts: "not_preserved",
    },
    runtime: {
      waza_version: "",
      requested_engine: "mock",
      requested_model: "mock-model",
      execution_mode: "mock",
      dependency_mode: "unknown",
      requested_policy: "deny_all",
      verified_enforcement: "unknown",
      go_version: "go1.26",
      platform: "linux/amd64",
      sdk_version: null,
      effective_model_version: null,
      no_skills: true,
      native_skill_control: "mock_not_applicable",
    },
    redaction: {
      policy: "default",
      applied_rules: ["sensitive-key"],
      match_count: 1,
      limitations: ["Pattern redaction is not a confidentiality proof."],
    },
    artifacts: [
      {
        id: "tool-events",
        kind: "tool_events",
        availability: "captured",
        completeness: "partial",
        document: "snapshot",
        pointer: "/toolEvents",
        content_digest: { sha256: "b".repeat(64), encoding: "json-v1" },
        reason: "The run recorded an operational error.",
      },
      {
        id: "workspace-file/report.txt",
        kind: "workspace_file",
        availability: "captured",
        completeness: "complete",
        document: "snapshot",
        pointer: "/workspaceFiles/0",
        content_digest: { sha256: "c".repeat(64), encoding: "utf8" },
        redacted: true,
      },
      {
        id: "task-config",
        kind: "task_configuration",
        availability: "digest_only",
        completeness: "unknown",
        source_digest: { sha256: "d".repeat(64), encoding: "json-v1" },
        reason: "Source identity only; no source bytes preserved.",
      },
      { id: "workspace", kind: "workspace_state", availability: "unavailable", completeness: "partial", reason: "Selected files do not preserve the entire workspace." },
      { id: "external-state", kind: "external_state", availability: "unavailable", completeness: "unknown", reason: "No authoritative external-state receipt." },
      { id: "lockfile", kind: "lockfile", availability: "unavailable", completeness: "unknown", reason: "No actual lockfile preserved." },
      { id: "grader-implementation", kind: "implementation_version", availability: "unavailable", completeness: "unknown", reason: "Grader implementation version unavailable." },
      { id: "rubric-version", kind: "rubric_version", availability: "unavailable", completeness: "unknown", reason: "Rubric version unavailable." },
      { id: "checkpoints", kind: "checkpoint_results", availability: "not_requested", completeness: "unknown", reason: "No checkpoint capture requested." },
    ],
    diagnostics: [{ category: "unknown", code: "run_error", message: "The run recorded an operational error; no agent cause is inferred." }],
  };
}

export function evidenceRun(runNumber = 1): EvidenceRun {
  const manifest = evidenceManifest(runNumber);
  return {
    runNumber,
    attempts: 2,
    cached: true,
    assessment: "recorded",
    message: "Producer metadata only; content availability must be verified for the requested operation.",
    manifest,
    explanations: [{
      task_id: "report-task",
      requirement_id: "required-report",
      checks: [{
        check: { scope: "task", grader: "report-check" },
        observation: "grader_recorded_failure",
        category: "operational_status_unavailable",
        message: "The executable grader recorded this result; typed operational provenance is unavailable, so an agent violation cannot be inferred.",
        references: [{ origin: manifest.origin, artifact_id: "tool-events", pointer: "/toolEvents/0" }],
      }, {
        check: { scope: "checkpoint", grader: "report-check", after_turn: 2 },
        observation: "unresolved",
        category: "insufficient_evidence",
        message: "No unique named check observation is available in the declared scope.",
      }],
    }],
  };
}

export function evidenceDetail(runs: EvidenceRun[] = [evidenceRun(), evidenceRun(2)]): RunDetail {
  return { ...RUN_DETAIL, tasks: [{ ...RUN_DETAIL.tasks[0], evidenceRuns: runs }] };
}
