import type { EnvironmentLogLine, Span } from "@/api/gen";

import { traceRef } from "../correlate";

export type Item =
  | { kind: "span"; key: string; time: number; span: Span }
  | { kind: "log"; key: string; time: number; line: EnvironmentLogLine };

export type Row = { item: Item; depth: number };

export function tree(spans: Span[], logs: EnvironmentLogLine[]): Row[] {
  const ids = new Set(spans.map((s) => s.spanId));
  const children = new Map<string, Item[]>();
  const roots: Item[] = [];
  const add = (parent: string | null, item: Item) => {
    if (parent === null) return void roots.push(item);
    const list = children.get(parent) ?? [];
    list.push(item);
    children.set(parent, list);
  };
  for (const span of spans) {
    const parented = span.parentId && span.parentId !== span.spanId && ids.has(span.parentId);
    add(parented ? span.parentId : null, {
      kind: "span",
      key: `span:${span.spanId}`,
      time: span.start,
      span,
    });
  }
  const firstRoot = spans.find((s) => !s.parentId || !ids.has(s.parentId))?.spanId ?? null;
  logs.forEach((line, i) => {
    const spanId = traceRef(line.text)?.spanId;
    const parent = spanId && ids.has(spanId) ? spanId : firstRoot;
    add(parent, {
      kind: "log",
      key: `log:${line.time}:${line.serviceId}:${i}`,
      time: line.time,
      line,
    });
  });

  const byTime = (a: Item, b: Item) => a.time - b.time;
  const rows: Row[] = [];
  const seen = new Set<string>();
  const walk = (item: Item, depth: number) => {
    if (seen.has(item.key)) return;
    seen.add(item.key);
    rows.push({ item, depth });
    if (item.kind === "span") {
      for (const child of (children.get(item.span.spanId) ?? []).sort(byTime))
        walk(child, depth + 1);
    }
  };
  for (const root of roots.sort(byTime)) walk(root, 0);
  for (const list of children.values()) for (const item of list) walk(item, 0);
  return rows;
}

export const itemEnd = (item: Item) =>
  item.kind === "span" ? item.span.start + item.span.duration : item.time;
