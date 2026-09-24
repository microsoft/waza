import {
  MessageSquare,
  Wrench,
  ArrowDownToLine,
  ArrowUpFromLine,
  Sigma,
  AlertTriangle,
} from "lucide-react";
import type { SessionDigest } from "../api/client";

function StatItem({
  icon: Icon,
  label,
  value,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string | number;
}) {
  return (
    <div className="flex items-center gap-2">
      <Icon className="h-4 w-4 text-zinc-500" />
      <div>
        <p className="text-xs text-zinc-500">{label}</p>
        <p className="text-sm font-medium text-zinc-200">{value}</p>
      </div>
    </div>
  );
}

export default function SessionDigestCard({
  digest,
}: {
  digest: SessionDigest;
}) {
  return (
    <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4 space-y-4">
      <h4 className="text-xs font-medium uppercase tracking-wider text-zinc-400">
        Session Digest
      </h4>

      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
        <StatItem
          icon={MessageSquare}
          label="Turns"
          value={digest.totalTurns}
        />
        <StatItem
          icon={Wrench}
          label="Tool Calls"
          value={digest.toolCallCount}
        />
        <StatItem
          icon={ArrowDownToLine}
          label="Tokens In"
          value={digest.tokensIn.toLocaleString()}
        />
        <StatItem
          icon={ArrowUpFromLine}
          label="Tokens Out"
          value={digest.tokensOut.toLocaleString()}
        />
        <StatItem
          icon={Sigma}
          label="Tokens Total"
          value={digest.tokensTotal.toLocaleString()}
        />
      </div>

      {digest.toolsUsed.length > 0 && (
        <div>
          <p className="mb-1.5 text-xs text-zinc-500">Tools Used</p>
          <div className="flex flex-wrap gap-1.5">
            {digest.toolsUsed.map((tool) => (
              <span
                key={tool}
                className="rounded bg-blue-500/10 px-2 py-0.5 text-xs font-medium text-blue-400"
              >
                {tool}
              </span>
            ))}
          </div>
        </div>
      )}

      {digest.toolPolicyMode && (
        <div>
          <p className="text-xs text-zinc-500">Tool Policy</p>
          <p className="text-sm text-zinc-200">{digest.toolPolicyMode}</p>
          {digest.toolPolicyDenials?.map((denial, i) => (
            <p key={i} className="mt-1 rounded bg-red-500/10 px-2 py-1 text-xs text-red-300">
              {denial.tool || "Unknown tool"} ({denial.kind}): {denial.reason}
            </p>
          ))}
        </div>
      )}

      {digest.errors.length > 0 && (
        <div>
          <p className="mb-1.5 flex items-center gap-1 text-xs text-red-400">
            <AlertTriangle className="h-3 w-3" />
            Errors ({digest.errors.length})
          </p>
          <div className="space-y-1">
            {digest.errors.map((err, i) => (
              <p
                key={i}
                className="rounded bg-red-500/10 px-2 py-1 text-xs text-red-300"
              >
                {err}
              </p>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
