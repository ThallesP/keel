import type { ReactNode } from "react";

export function Tooltip({
  x,
  width,
  title,
  children,
}: {
  x: number;
  width: number;
  title: string;
  children: ReactNode;
}) {
  const right = x > width - 170;
  return (
    <div
      className="pointer-events-none absolute top-1 z-10 min-w-32 rounded-md border border-line bg-bg px-2.5 py-2 text-2xs shadow-[0_4px_16px_rgba(11,18,32,0.08)]"
      style={right ? { right: width - x + 10 } : { left: x + 10 }}
    >
      <div className="mb-1 font-mono text-faint">{title}</div>
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  );
}
