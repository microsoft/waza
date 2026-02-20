interface TimelineAxisProps {
  totalEvents: number;
  labelWidth: number;
}

export default function TimelineAxis({
  totalEvents,
  labelWidth,
}: TimelineAxisProps) {
  const step = totalEvents <= 20 ? 5 : totalEvents <= 50 ? 10 : 25;
  const ticks: number[] = [];
  for (let i = 0; i <= totalEvents; i += step) ticks.push(i);
  if (ticks[ticks.length - 1] !== totalEvents) ticks.push(totalEvents);

  return (
    <div className="sticky top-0 z-10 flex bg-zinc-900 border-b border-zinc-700">
      <div
        className="shrink-0 border-r border-zinc-700"
        style={{ width: labelWidth }}
      />
      <div className="relative flex-1 h-6">
        {ticks.map((t) => {
          const pct = totalEvents > 0 ? (t / totalEvents) * 100 : 0;
          return (
            <div
              key={t}
              className="absolute top-0 flex flex-col items-center"
              style={{ left: `${pct}%` }}
            >
              <div className="h-3 w-px bg-zinc-700" />
              <span className="text-[10px] text-zinc-400 -translate-x-1/2">
                {t}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
