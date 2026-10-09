import { BASE, LEFT, RIGHT } from "./layout";

export function Grid({ width, ticks }: { width: number; ticks: [number, string][] }) {
  return (
    <g>
      {ticks.map(([offset, label]) => {
        const y = BASE - offset;
        return (
          <g key={label}>
            <line
              x1={LEFT}
              x2={width - RIGHT}
              y1={y}
              y2={y}
              stroke={offset === 0 ? "var(--color-dot)" : "var(--color-line)"}
              strokeWidth={1}
              shapeRendering="crispEdges"
            />
            <text
              x={LEFT - 8}
              y={y}
              dy="0.32em"
              textAnchor="end"
              className="fill-faint font-mono text-2xs tabular-nums"
            >
              {label}
            </text>
          </g>
        );
      })}
    </g>
  );
}
