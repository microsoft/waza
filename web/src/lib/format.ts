export function formatDuration(seconds: number): string {
  if (seconds <= 0) return "0s";
  if (seconds < 1) return "<1s";

  const rounded = Math.round(seconds);
  const m = Math.floor(rounded / 60);
  const s = rounded % 60;

  if (m === 0) return `${s}s`;
  return `${m}m ${s}s`;
}

export function formatCost(dollars: number): string {
  return `$${dollars.toFixed(2)}`;
}

// AI_CREDITS_TOOLTIP explains what the "AI Credits" metric represents. The value
// is the final AI-credit usage the Copilot SDK reports for the session(s) this
// waza run started — it does not include Copilot usage from anywhere else in
// the account.
export const AI_CREDITS_TOOLTIP =
  "Final AI Credit usage reported by the Copilot SDK for this waza run — not account-wide Copilot usage.";

// AI_CREDITS_UNAVAILABLE_TOOLTIP is shown when a run carries no authoritative
// AI-credit total (legacy result artifacts, or Copilot runtimes that don't
// report final credit metrics). waza never substitutes an estimate here.
export const AI_CREDITS_UNAVAILABLE_TOOLTIP =
  "AI Credit usage unavailable — at least one session did not report final metrics (legacy runtime, custom provider, or missing usage). No estimate is substituted.";

export const AVG_AI_CREDITS_TOOLTIP =
  "Average final AI Credit usage across only runs with complete reported totals. Runs with unavailable totals are excluded from the denominator; this is not account-wide Copilot usage.";

export const AVG_AI_CREDITS_UNAVAILABLE_TOOLTIP =
  "Average AI Credit usage unavailable — none of the runs report a complete final total. No estimate is substituted.";

// AI_CREDITS_UNAVAILABLE is the placeholder rendered for runs and models with
// no authoritative AI-credit total.
export const AI_CREDITS_UNAVAILABLE = "—";

// formatAICredits renders an AI-credit amount reported by the Copilot SDK.
// Returns an explicit unavailable marker when the backend omitted the value.
export function formatAICredits(credits?: number | null): string {
  if (credits == null || !Number.isFinite(credits)) {
    return AI_CREDITS_UNAVAILABLE;
  }
  return credits.toLocaleString("en-US", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 9,
  });
}

export function costSourceTooltip(source?: string): string {
  switch (source) {
    case "sdk":
      return "Reported by Copilot SDK";
    case "table":
      return "Calculated from model rate table (as of 2025-01-01)";
    case "estimate":
      return "Rough flat-rate estimate ($0.00025/token) — model pricing unavailable";
    case "mixed":
      return "Mixed sources across runs — hover individual rows for details";
    default:
      // An omitted/empty costSource means the backend had no usage data to
      // price (e.g. legacy ResultSummary rows). Don't claim it was estimated.
      return "Cost data unavailable for this run";
  }
}

export function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return n.toString();
}

export function formatRelativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const seconds = Math.floor(diff / 1000);

  if (seconds < 60) return "just now";

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;

  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;

  const days = Math.floor(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

export function formatPercent(ratio: number): string {
  return `${Math.round(ratio * 100)}%`;
}
