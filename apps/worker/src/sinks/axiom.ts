import { errorText, log, sleep } from "../log";
import type { LogEvent, Sink, SinkConfig } from "./types";

// Axiom sink: POST NDJSON to the dataset's ingest endpoint. Field names are kept as-is; `_time`
// is the event timestamp Axiom indexes on. Basic (ingest-only) API tokens are enough for this
// side. The path depends on the host, as in axiom-go: `api.axiom.co` / `api.eu.axiom.co` take
// /v1/datasets/<dataset>/ingest (/v1/ingest/<dataset> is 404 there); regional edge hosts
// (`*.edge.axiom.co`) take /v1/ingest/<dataset>.

const baseUrl = (domain: string) =>
  (domain.includes("://") ? domain : `https://${domain}`).replace(/\/+$/, "");

export function axiomSink(cfg: SinkConfig & { kind: "axiom" }, key: string): Sink {
  const dataset = encodeURIComponent(cfg.dataset);
  const url = cfg.domain.endsWith(".edge.axiom.co")
    ? `${baseUrl(cfg.domain)}/v1/ingest/${dataset}`
    : `${baseUrl(cfg.domain)}/v1/datasets/${dataset}/ingest`;
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
          if (res.ok) return;
          const text = (await res.text().catch(() => "")).slice(0, 200);
          if (res.status >= 400 && res.status < 500 && res.status !== 429) {
            log("axiom", `rejected ${res.status}, dropping ${events.length} events`, { text });
            return;
          }
          log("axiom", `ingest ${res.status}, retry ${attempt + 1}`, { text });
        } catch (err) {
          log("axiom", `ingest failed (${errorText(err)}), retry ${attempt + 1}`);
        }
        await sleep(1000 * 2 ** attempt);
      }
      log("axiom", `giving up on ${events.length} events`);
    },
  };
}
