import type { Span } from "@/gen/api";

import { formatDuration } from "../../format";
import { Pairs } from "./pairs";
import { Section } from "./section";

export function SpanDetail({ span, traceStart }: { span: Span; traceStart: number }) {
  return (
    <div className="flex flex-col gap-5 px-4 py-3">
      <div>
        <div className="text-sm font-semibold break-words text-ink">{span.name || "(unnamed)"}</div>
        <div className="mt-0.5 text-2xs text-muted-foreground">
          {[span.service, span.kind].filter(Boolean).join(" · ")}
        </div>
      </div>
      <Pairs
        pairs={[
          { key: "duration", value: formatDuration(span.duration) },
          { key: "starts at", value: `+${formatDuration(span.start - traceStart)}` },
          { key: "status", value: span.status },
          { key: "span", value: span.spanId },
          { key: "parent", value: span.parentId || "—" },
          { key: "scope", value: span.scope || "—" },
        ]}
      />
      {span.statusMessage && (
        <p className="rounded-md bg-danger-soft px-2.5 py-2 font-mono text-2xs break-words whitespace-pre-wrap text-danger">
          {span.statusMessage}
        </p>
      )}
      <Section title="Attributes" empty="No attributes">
        {span.attributes.length > 0 && <Pairs pairs={span.attributes} />}
      </Section>
      {span.events.length > 0 && (
        <Section title="Events">
          {span.events.map((e, i) => (
            <div key={`${e.time}:${i}`} className="flex flex-col gap-1.5">
              <div className="flex items-baseline justify-between gap-3 text-xs">
                <span className="truncate text-ink">{e.name || "(unnamed)"}</span>
                <span className="shrink-0 font-mono text-2xs text-faint">
                  +{formatDuration(Math.max(0, e.time - traceStart))}
                </span>
              </div>
              {e.attributes.length > 0 && <Pairs pairs={e.attributes} />}
            </div>
          ))}
        </Section>
      )}
      <Section title="Resource" empty="No resource attributes">
        {span.resource.length > 0 && <Pairs pairs={span.resource} />}
      </Section>
    </div>
  );
}
