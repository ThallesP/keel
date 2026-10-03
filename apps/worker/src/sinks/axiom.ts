import { errorText, log, sleep } from "../log";
import type { LogEvent, Sink, SinkConfig } from "./types";

// Axiom sink: POST NDJSON to /v1/ingest/<dataset>. Field names are kept as-is; `_time` is the
// event timestamp Axiom indexes on. Basic (ingest-only) API tokens are enough for this side.

const baseUrl = (domain: string) =>
  (domain.includes("://") ? domain : `https://${domain}`).replace(/\/+$/, "");

export function axiomSink(cfg: SinkConfig & { kind: "axiom" }, key: string): Sink {
  const url = `${baseUrl(cfg.domain)}/v1/ingest/${encodeURIComponent(cfg.dataset)}`;
  return {
    key,
    async send(events: LogEvent[]) {
      const body = events.map((e) => JSON.stringify(e)).join("\n");
      for (let attempt = 0; attempt < 5; attempt++) {
        try {
          const res = await fetch(url, {
            method: "POST",
            headers: {
              authorization: `Bearer ${cfg.token}`,
              "content-type": "application/x-ndjson",
            },
            body,
            signal: AbortSignal.timeout(15_000),
          });
          if (res.ok) return true;
          const text = (await res.text().catch(() => "")).slice(0, 200);
          if (res.status >= 400 && res.status < 500 && res.status !== 429) {
            log("axiom", `rejected ${res.status}, dropping ${events.length} events`, { text });
            return true;
          }
          log("axiom", `ingest ${res.status}, retry ${attempt + 1}`, { text });
        } catch (err) {
          log("axiom", `ingest failed (${errorText(err)}), retry ${attempt + 1}`);
        }
        await sleep(1000 * 2 ** attempt);
      }
      log("axiom", `unreachable, keeping ${events.length} events for a later attempt`);
      return false;
    },
  };
}
