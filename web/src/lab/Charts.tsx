import { percent, shortDate, summarize, type LabRun, type Model } from "./data";

function modelColor(model: Model) {
  if (model === "gpt-5.4") return "#4163da";
  if (model === "claude-sonnet-4.6") return "#279a86";
  const palette = ["#4163da", "#279a86", "#9255a5", "#bd7020"];
  const hash = [...model].reduce(
    (sum, letter) => sum + letter.charCodeAt(0),
    0,
  );
  return palette[hash % palette.length];
}

export function QualityChart({
  runs,
  selectedDay,
  onSelect,
}: {
  runs: LabRun[];
  selectedDay: string | null;
  onSelect: (day: string) => void;
}) {
  const dates = [...new Set(runs.map((run) => run.date.slice(0, 10)))].sort();
  const models = [...new Set(runs.map((run) => run.model))].sort();
  const width = 680;
  const height = 230;
  const x = (index: number) =>
    45 + (index / Math.max(1, dates.length - 1)) * 610;
  const y = (score: number) => 190 - (score / 100) * 165;
  return (
    <div className="lab-chart">
      <svg
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label="Weighted score by day and model. Use the date buttons below to filter runs."
      >
        {[0, 25, 50, 75, 100].map((score) => (
          <g key={score}>
            <line
              x1="45"
              x2="655"
              y1={y(score)}
              y2={y(score)}
              stroke="#e9edf3"
            />
            <text x="3" y={y(score) + 4}>
              {score}%
            </text>
          </g>
        ))}
        {models.map((model) => {
          const points = dates
            .map((date, index) => {
              const score = summarize(
                runs.filter(
                  (run) => run.model === model && run.date.startsWith(date),
                ),
              ).score;
              return score === null
                ? null
                : { date, score, x: x(index), y: y(score) };
            })
            .filter((point) => point !== null);
          return (
            <g key={model}>
              <polyline
                fill="none"
                stroke={modelColor(model)}
                strokeWidth="2.5"
                points={points
                  .map((point) => `${point.x},${point.y}`)
                  .join(" ")}
              />
              {points.map((point) => (
                <g key={point.date}>
                  <circle
                    cx={point.x}
                    cy={point.y}
                    r={selectedDay === point.date ? 6 : 4}
                    fill={modelColor(model)}
                    stroke="white"
                    strokeWidth="2"
                  />
                  <circle
                    cx={point.x}
                    cy={point.y}
                    r="12"
                    fill="transparent"
                    className="lab-chart-hit"
                    onClick={() => onSelect(point.date)}
                  >
                    <title>
                      {model}, {shortDate(point.date)}: {percent(point.score)}.
                      Select this day.
                    </title>
                  </circle>
                </g>
              ))}
            </g>
          );
        })}
        {dates.map((date, index) =>
          index % 2 === 0 || index === dates.length - 1 ? (
            <text key={date} x={x(index)} y="217" textAnchor="middle">
              {shortDate(date)}
            </text>
          ) : null,
        )}
      </svg>
      <div className="lab-chart-dates" aria-label="Filter by chart date">
        {dates.map((date) => (
          <button
            key={date}
            aria-pressed={selectedDay === date}
            onClick={() => onSelect(date)}
          >
            {shortDate(date)}
          </button>
        ))}
      </div>
      <div className="lab-chart-legend">
        {models
          .filter((model) => runs.some((run) => run.model === model))
          .map((model) => (
            <span key={model}>
              <i style={{ background: modelColor(model) }} />
              {model}
            </span>
          ))}
        <span className="lab-muted">
          Select a point or date to explore its runs
        </span>
      </div>
    </div>
  );
}

export function ModelChart({
  runs,
  onSelect,
}: {
  runs: LabRun[];
  onSelect: (model: Model) => void;
}) {
  const models = [...new Set(runs.map((run) => run.model))].sort();
  return (
    <div className="lab-model-chart">
      {models
        .filter((model) => runs.some((run) => run.model === model))
        .map((model) => {
          const stats = summarize(runs.filter((run) => run.model === model));
          return (
            <button
              key={model}
              onClick={() => onSelect(model)}
              aria-label={`Filter to ${model}`}
            >
              <span>
                {model}
                <strong>{percent(stats.passRate)}</strong>
              </span>
              <div className="lab-bar-track">
                <div
                  style={{
                    width: `${stats.passRate ?? 0}%`,
                    background: modelColor(model),
                  }}
                />
              </div>
              <small>
                {stats.runs} runs · {stats.tasks} task results
              </small>
            </button>
          );
        })}
    </div>
  );
}
