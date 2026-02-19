import {
  Activity,
  ListChecks,
  CheckCircle2,
  Coins,
  DollarSign,
  Clock,
} from "lucide-react";
import type { SummaryResponse } from "../api/client";
import {
  formatNumber,
  formatCost,
  formatDuration,
} from "../lib/format";

function passRateColor(rate: number): string {
  if (rate >= 80) return "text-green-500";
  if (rate >= 50) return "text-yellow-500";
  return "text-red-500";
}

interface CardProps {
  label: string;
  value: string;
  icon: React.ReactNode;
  valueClass?: string;
}

function Card({ label, value, icon, valueClass }: CardProps) {
  return (
    <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
      <div className="flex items-center justify-between">
        <span className="text-sm text-zinc-400">{label}</span>
        <span className="text-zinc-500">{icon}</span>
      </div>
      <p className={`mt-2 text-2xl font-semibold ${valueClass ?? "text-zinc-100"}`}>
        {value}
      </p>
    </div>
  );
}

function SkeletonCard() {
  return (
    <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
      <div className="flex items-center justify-between">
        <div className="h-4 w-20 rounded bg-zinc-700" />
        <div className="h-5 w-5 rounded bg-zinc-700" />
      </div>
      <div className="mt-2 h-8 w-24 rounded bg-zinc-700" />
    </div>
  );
}

export function KPICardsSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
      {Array.from({ length: 6 }).map((_, i) => (
        <SkeletonCard key={i} />
      ))}
    </div>
  );
}

export default function KPICards({ data }: { data: SummaryResponse }) {
  const iconSize = "h-5 w-5";
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
      <Card
        label="Total Runs"
        value={data.totalRuns.toString()}
        icon={<Activity className={iconSize} />}
      />
      <Card
        label="Total Tasks"
        value={data.totalTasks.toString()}
        icon={<ListChecks className={iconSize} />}
      />
      <Card
        label="Pass Rate"
        value={`${Math.round(data.passRate)}%`}
        icon={<CheckCircle2 className={iconSize} />}
        valueClass={passRateColor(data.passRate)}
      />
      <Card
        label="Avg Tokens"
        value={formatNumber(data.avgTokens)}
        icon={<Coins className={iconSize} />}
      />
      <Card
        label="Avg Cost"
        value={formatCost(data.avgCost)}
        icon={<DollarSign className={iconSize} />}
      />
      <Card
        label="Avg Duration"
        value={formatDuration(data.avgDuration)}
        icon={<Clock className={iconSize} />}
      />
    </div>
  );
}
