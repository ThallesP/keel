export function Series({
  color,
  points,
  dashed = false,
}: {
  color: string;
  points: ([number, number] | null)[];
  dashed?: boolean;
}) {
  const runs: [number, number][][] = [];
  let run: [number, number][] = [];
  for (const p of points) {
    if (p) run.push(p);
    else if (run.length) {
      runs.push(run);
      run = [];
    }
  }
  if (run.length) runs.push(run);
  return (
    <g>
      {runs.map((r) =>
        r.length === 1 ? (
          <circle key={r[0]![0]} cx={r[0]![0]} cy={r[0]![1]} r={2.5} fill={color} />
        ) : (
          <path
            key={r[0]![0]}
            d={r.map(([px, py], i) => `${i ? "L" : "M"}${px},${py}`).join("")}
            fill="none"
            stroke={color}
            strokeWidth={2}
            strokeDasharray={dashed ? "4 3" : undefined}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ),
      )}
    </g>
  );
}
