import { useState, useRef, useEffect } from "react";

export interface DataPoint {
  label: string;
  value: number;
}

interface TrendChartProps {
  title: string;
  data: DataPoint[];
  formatValue: (v: number) => string;
}

const PADDING = { top: 20, right: 16, bottom: 32, left: 56 };
const CHART_HEIGHT = 200;
const GRID_LINES = 5;

export default function TrendChart({
  title,
  data,
  formatValue,
}: TrendChartProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [tooltip, setTooltip] = useState<{
    x: number;
    y: number;
    point: DataPoint;
  } | null>(null);
  const [svgWidth, setSvgWidth] = useState(400);

  useEffect(() => {
    const node = svgRef.current;
    if (!node) return;
    setSvgWidth(node.clientWidth);
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setSvgWidth(entry.contentRect.width);
      }
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  if (data.length === 0) {
    return (
      <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
        <h3 className="text-sm font-medium text-zinc-300">{title}</h3>
        <div className="flex h-[200px] items-center justify-center text-sm text-zinc-500">
          No data available
        </div>
      </div>
    );
  }

  const values = data.map((d) => d.value);
  const minVal = Math.min(...values);
  const maxVal = Math.max(...values);
  const range = maxVal - minVal || 1;
  const yMin = minVal - range * 0.1;
  const yMax = maxVal + range * 0.1;
  const yRange = yMax - yMin || 1;

  const plotW = svgWidth - PADDING.left - PADDING.right;
  const plotH = CHART_HEIGHT - PADDING.top - PADDING.bottom;

  const toX = (i: number) =>
    PADDING.left + (data.length === 1 ? plotW / 2 : (i / (data.length - 1)) * plotW);
  const toY = (v: number) =>
    PADDING.top + plotH - ((v - yMin) / yRange) * plotH;

  const linePath = data
    .map((d, i) => `${i === 0 ? "M" : "L"} ${toX(i)} ${toY(d.value)}`)
    .join(" ");

  const areaPath = `${linePath} L ${toX(data.length - 1)} ${PADDING.top + plotH} L ${toX(0)} ${PADDING.top + plotH} Z`;

  const gridYValues = Array.from({ length: GRID_LINES }, (_, i) =>
    yMin + (yRange * i) / (GRID_LINES - 1),
  );

  const handleMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const svg = svgRef.current;
    if (!svg || data.length === 0) return;
    const rect = svg.getBoundingClientRect();
    const mx = e.clientX - rect.left;

    let closest = 0;
    let closestDist = Infinity;
    for (let i = 0; i < data.length; i++) {
      const dist = Math.abs(toX(i) - mx);
      if (dist < closestDist) {
        closestDist = dist;
        closest = i;
      }
    }

    const pt = data[closest];
    if (!pt) return;
    setTooltip({
      x: toX(closest),
      y: toY(pt.value),
      point: pt,
    });
  };

  return (
    <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
      <h3 className="mb-2 text-sm font-medium text-zinc-300">{title}</h3>
      <svg
        ref={svgRef}
        width="100%"
        height={CHART_HEIGHT}
        className="overflow-visible"
        onMouseMove={handleMouseMove}
        onMouseLeave={() => setTooltip(null)}
      >
        {/* Grid lines */}
        {gridYValues.map((v, i) => (
          <g key={i}>
            <line
              x1={PADDING.left}
              x2={svgWidth - PADDING.right}
              y1={toY(v)}
              y2={toY(v)}
              stroke="rgb(63 63 70)" // zinc-700
              strokeDasharray="4 4"
            />
            <text
              x={PADDING.left - 8}
              y={toY(v) + 4}
              textAnchor="end"
              className="fill-zinc-500 text-[10px]"
            >
              {formatValue(v)}
            </text>
          </g>
        ))}

        {/* X-axis labels (first, middle, last) */}
        {data.length > 0 && (
          <>
            <text
              x={toX(0)}
              y={CHART_HEIGHT - 4}
              textAnchor="start"
              className="fill-zinc-500 text-[10px]"
            >
              {data[0]?.label}
            </text>
            {data.length > 2 && (
              <text
                x={toX(Math.floor(data.length / 2))}
                y={CHART_HEIGHT - 4}
                textAnchor="middle"
                className="fill-zinc-500 text-[10px]"
              >
                {data[Math.floor(data.length / 2)]?.label}
              </text>
            )}
            {data.length > 1 && (
              <text
                x={toX(data.length - 1)}
                y={CHART_HEIGHT - 4}
                textAnchor="end"
                className="fill-zinc-500 text-[10px]"
              >
                {data[data.length - 1]?.label}
              </text>
            )}
          </>
        )}

        {/* Area fill */}
        <path d={areaPath} fill="rgb(59 130 246 / 0.1)" />

        {/* Line */}
        <path
          d={linePath}
          fill="none"
          stroke="rgb(59 130 246)" // blue-500
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
        />

        {/* Data points */}
        {data.map((d, i) => (
          <circle
            key={i}
            cx={toX(i)}
            cy={toY(d.value)}
            r={3}
            fill="rgb(59 130 246)"
            stroke="rgb(39 39 42)" // zinc-800
            strokeWidth={2}
          />
        ))}

        {/* Tooltip crosshair & dot */}
        {tooltip && (
          <>
            <line
              x1={tooltip.x}
              x2={tooltip.x}
              y1={PADDING.top}
              y2={PADDING.top + plotH}
              stroke="rgb(113 113 122)" // zinc-500
              strokeDasharray="2 2"
            />
            <circle
              cx={tooltip.x}
              cy={tooltip.y}
              r={5}
              fill="rgb(59 130 246)"
              stroke="rgb(24 24 27)" // zinc-900
              strokeWidth={2}
            />
          </>
        )}
      </svg>

      {/* Tooltip overlay */}
      {tooltip && (
        <div
          className="pointer-events-none absolute z-10 rounded bg-zinc-700 px-2 py-1 text-xs text-zinc-100 shadow-lg"
          style={{
            left: tooltip.x,
            top: tooltip.y - 36,
            transform: "translateX(-50%)",
            position: "absolute",
          }}
        >
          {formatValue(tooltip.point.value)}
          <span className="ml-1 text-zinc-400">{tooltip.point.label}</span>
        </div>
      )}
    </div>
  );
}
