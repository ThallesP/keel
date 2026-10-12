import { type Band, HEIGHT, type Hover } from "./layout";

export function HitColumns({
  x,
  count,
  onHover,
}: {
  x: Band;
  count: number;
  onHover: (i: Hover) => void;
}) {
  return (
    <g>
      {Array.from({ length: count }, (_, i) => (
        <rect
          key={i}
          x={x.start(i)}
          y={0}
          width={x.band}
          height={HEIGHT}
          fill="transparent"
          onPointerEnter={() => onHover(i)}
        />
      ))}
    </g>
  );
}
