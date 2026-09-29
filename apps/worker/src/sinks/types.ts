// A sink receives batches of log events for one project. The worker never knows what the sink
// does with them; the control plane's read side (packages/backend/convex/logProviders/<kind>.ts)
// must understand the same event shape. Field names are the contract (docs/logs.md):
//
//   _time       RFC3339Nano from Docker's timestamps=1
//   message     the line, without the timestamp
//   stream      "stdout" | "stderr"
//   service_id  Convex node id (the `svc-<id>` service, minus the prefix)
//   service     Swarm service name (`svc-<id>`)
//   task        Swarm task id (one per replica run)
//   replica     Swarm slot number, from the task name `svc-<id>.<slot>.<task>`
//   node        Swarm node id this worker runs on
//   container   short container id

export type LogEvent = {
  _time: string;
  message: string;
  stream: "stdout" | "stderr";
  service_id: string;
  service: string;
  task: string;
  replica: number;
  node: string;
  container: string;
};

export interface Sink {
  readonly key: string;
  /** Delivers a batch. Must not throw on transient failure: retry inside, drop with a log on 4xx. */
  send(events: LogEvent[]): Promise<void>;
}

export type SinkConfig = { kind: "axiom"; domain: string; dataset: string; token: string };

/** Stable identity for a config, so a changed token or dataset builds a new sink. */
export const sinkKey = (c: SinkConfig) => `${c.kind}:${c.domain}:${c.dataset}:${c.token.slice(-6)}`;
