import { Info } from "lucide-react";

interface InfoTooltipProps {
  text: string;
  className?: string;
  size?: number;
}

/**
 * Small inline info icon with a native-browser tooltip on hover/focus.
 * Used for clarifying data sources / accuracy disclaimers (e.g. cost source).
 * Rendered as a button so it's reachable by keyboard users.
 */
export function InfoTooltip({ text, className = "", size = 12 }: InfoTooltipProps) {
  return (
    <button
      type="button"
      title={text}
      aria-label={text}
      className={`inline-flex items-center text-zinc-500 hover:text-zinc-300 cursor-help bg-transparent border-0 p-0 ${className}`}
      style={{ verticalAlign: "middle" }}
    >
      <Info size={size} aria-hidden="true" />
    </button>
  );
}
