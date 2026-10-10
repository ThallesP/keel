import { cn } from "@my-better-t-app/ui/lib/utils";

import type { EnvironmentLogLine, Span } from "@/gen/api";
import { AnsiText } from "@/lib/ansi";

import { formatDuration } from "../../format";
import { SectionLabel } from "../../primitives";
import { type ServiceLabel, useServices } from "../chrome";
import type { Row } from "./tree";

const NAME_COL = "w-[38%] min-w-[240px] max-w-[440px] shrink-0";

export function Waterfall({
  rows,
  start,
  total,
  selected,
  onSelect,
}: {
  rows: Row[];
  start: number;
  total: number;
  selected: string;
  onSelect: (key: string) => void;
}) {
  const services = useServices();
  return (
    <div className="min-w-0 flex-1 overflow-auto">
      <div className="sticky top-0 z-10 flex h-8 items-center border-b border-line bg-bg">
        <span className={cn(NAME_COL, "pl-5")}>
          <SectionLabel>Spans and logs</SectionLabel>
        </span>
        <span className="relative mr-5 h-full flex-1">
          {[0, 0.25, 0.5, 0.75, 1].map((q) => (
            <span
              key={q}
              className={cn(
                "absolute top-1/2 -translate-y-1/2 font-mono text-2xs text-faint tabular-nums",
                q === 1 ? "-translate-x-full" : q > 0 && "-translate-x-1/2",
              )}
              style={{ left: `${q * 100}%` }}
            >
              {q === 0 ? "0" : formatDuration(total * q)}
            </span>
          ))}
        </span>
      </div>
      {rows.map(({ item, depth }) => {
        const isSelected = item.key === selected;
        return (
          <button
            key={item.key}
            type="button"
            aria-current={isSelected ? "true" : undefined}
            onClick={() => onSelect(item.key)}
            className={cn(
              "flex h-7 w-full items-center text-left text-xs",
              isSelected ? "bg-primary-soft" : "hover:bg-surface-2",
            )}
          >
            <span
              className={cn(NAME_COL, "flex min-w-0 items-center gap-2 pr-3")}
              style={{ paddingLeft: 20 + depth * 14 }}
            >
              {item.kind === "span" ? (
                <SpanName span={item.span} service={services.ofSpan(item.span.service)} />
              ) : (
                <LineName line={item.line} service={services.ofLine(item.line.serviceId)} />
              )}
            </span>
            <span
              className={cn(
                "relative mr-5 h-full flex-1",
                "bg-[linear-gradient(to_right,var(--color-line)_1px,transparent_1px)] bg-[length:25%_100%]",
              )}
            >
              {item.kind === "span" ? (
                <SpanBar span={item.span} start={start} total={total} />
              ) : (
                <LinePoint line={item.line} start={start} total={total} />
              )}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function SpanName({ span, service }: { span: Span; service: ServiceLabel }) {
  return (
    <>
      {span.status === "error" && (
        <span className="size-1.5 shrink-0 rounded-full bg-danger" aria-label="error" />
      )}
      <span className={cn("max-w-[45%] shrink-0 truncate font-mono text-2xs", service.tone)}>
        {service.text}
      </span>
      <span className="truncate text-ink">{span.name || "(unnamed)"}</span>
    </>
  );
}

function LineName({ line, service }: { line: EnvironmentLogLine; service: ServiceLabel }) {
  return (
    <>
      <span className="shrink-0 rounded-sm bg-surface-2 px-1 font-mono text-[10px] text-muted-foreground">
        log
      </span>
      <span className={cn("max-w-[30%] shrink-0 truncate font-mono text-2xs", service.tone)}>
        {service.text}
      </span>
      <span
        className={cn(
          "truncate font-mono text-2xs",
          line.stream === "stderr" ? "text-warning" : "text-muted-foreground",
        )}
      >
        <AnsiText text={line.text} />
      </span>
    </>
  );
}

function labelPlace(left: number, width: number) {
  if (left + width <= 80) return "after";
  if (width >= 20) return "inside";
  return "before";
}

function SpanBar({ span, start, total }: { span: Span; start: number; total: number }) {
  const left = ((span.start - start) / total) * 100;
  const width = (span.duration / total) * 100;
  const failed = span.status === "error";
  const labelStyle = {
    after: { left: `calc(${left + width}% + 6px)` },
    inside: { right: `calc(${100 - left - width}% + 6px)` },
    before: { right: `calc(${100 - left}% + 6px)` },
  };
  const place = labelPlace(left, width);
  return (
    <>
      <span
        className={cn(
          "absolute top-1/2 h-2.5 -translate-y-1/2 rounded-sm",
          failed ? "bg-danger" : "bg-primary",
        )}
        style={{ left: `${left}%`, width: `max(2px, ${width}%)` }}
      />
      <span
        className={cn(
          "absolute top-1/2 -translate-y-1/2 font-mono text-2xs whitespace-nowrap tabular-nums",
          place === "inside" ? "text-white" : "text-muted-foreground",
        )}
        style={labelStyle[place]}
      >
        {formatDuration(span.duration)}
      </span>
    </>
  );
}

function LinePoint({
  line,
  start,
  total,
}: {
  line: EnvironmentLogLine;
  start: number;
  total: number;
}) {
  return (
    <span
      className={cn(
        "absolute top-1/2 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-bg",
        line.stream === "stderr" ? "bg-warning" : "bg-muted-foreground",
      )}
      style={{ left: `${Math.min(100, Math.max(0, ((line.time - start) / total) * 100))}%` }}
    />
  );
}
