import type { ReactNode } from "react";

export function ChartCard({
  title,
  legend,
  table,
  children,
}: {
  title: string;
  legend: ReactNode;
  table: ReactNode;
  children: ReactNode;
}) {
  return (
    <figure className="flex min-w-0 flex-col rounded-lg border border-line px-4 pt-3 pb-1">
      <figcaption className="flex items-center justify-between">
        <span className="text-xs font-medium text-ink">{title}</span>
        <span className="flex items-center gap-3">{legend}</span>
      </figcaption>
      <div className="mt-2">{children}</div>
      {table}
    </figure>
  );
}
