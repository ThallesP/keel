import { axiomSink } from "./axiom";
import { type Sink, type SinkConfig, sinkKey } from "./types";

export type { LogEvent, Sink, SinkConfig } from "./types";
export { sinkKey };

/** The one place that knows every sink kind. */
export function buildSink(cfg: SinkConfig): Sink {
  switch (cfg.kind) {
    case "axiom":
      return axiomSink(cfg, sinkKey(cfg));
  }
}
