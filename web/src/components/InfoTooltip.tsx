import { Info } from "lucide-react";

interface InfoTooltipProps {
  text: string;
  className?: string;
  size?: number;
}

/**
 * Small inline info icon with a native-browser tooltip on hover.
 * Used for clarifying data sources / accuracy disclaimers (e.g. cost source).
 */
export function InfoTooltip({ text, className = "", size = 12 }: InfoTooltipProps) {
  return (
    <span
      title={text}
      aria-label={text}
      role="img"
      className={`inline-flex items-center text-muted-foreground cursor-help ${className}`}
      style={{ verticalAlign: "middle" }}
    >
      <Info size={size} aria-hidden="true" />
    </span>
  );
}
