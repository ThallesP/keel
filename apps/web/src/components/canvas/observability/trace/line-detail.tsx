import { cn } from "@my-better-t-app/ui/lib/utils";

import type { Attribute, EnvironmentLogLine } from "@/gen/api";
import { AnsiText } from "@/lib/ansi";

import { formatDuration, formatLogTime } from "../../format";
import { useServices } from "../chrome";
import { lineFields } from "../correlate";
import { Pairs } from "./pairs";
import { Section } from "./section";

export function LineDetail({
  line,
  traceStart,
}: {
  line: EnvironmentLogLine;
  traceStart?: number;
}) {
  const services = useServices();
  const fields = lineFields(line.text);
  const facts: Attribute[] = [{ key: "time", value: formatLogTime(line.time) }];
  if (traceStart !== undefined) {
    facts.push({
      key: "in trace",
      value: `+${formatDuration(Math.max(0, line.time - traceStart))}`,
    });
  }
  facts.push({ key: "task", value: line.task || "—" });
  return (
    <div className="flex flex-col gap-5 px-4 py-3">
      <div>
        <div className="text-sm font-semibold text-ink">Log line</div>
        <div className="mt-0.5 text-2xs text-muted-foreground">
          {services.ofLine(line.serviceId).text} · {line.stream}
        </div>
      </div>
      <p
        className={cn(
          "rounded-md bg-surface-2 px-2.5 py-2 font-mono text-2xs break-all whitespace-pre-wrap",
          line.stream === "stderr" ? "text-warning" : "text-ink",
        )}
      >
        <AnsiText text={line.text} />
      </p>
      <Pairs pairs={facts} />
      {fields.length > 0 && (
        <Section title="Fields">
          <Pairs pairs={fields} />
        </Section>
      )}
    </div>
  );
}
