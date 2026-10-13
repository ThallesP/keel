import type { Panel } from "@/gen/api";

type Kind = Panel["kind"];

export const PANEL_KINDS: Record<
  Kind,
  { kind: Kind; label: string; wide: boolean; perService: boolean }
> = {
  cpu: { kind: "cpu", label: "CPU", wide: false, perService: true },
  memory: { kind: "memory", label: "Memory", wide: false, perService: true },
  network: { kind: "network", label: "Network", wide: false, perService: true },
  requests: { kind: "requests", label: "Requests", wide: false, perService: true },
  latency: { kind: "latency", label: "Latency", wide: false, perService: true },
  logs: { kind: "logs", label: "Logs", wide: true, perService: false },
  servers: { kind: "servers", label: "Servers", wide: false, perService: false },
};
