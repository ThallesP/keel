import { getRouteApi } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useMemo } from "react";

import { useListNodes } from "@/gen/api";

import { useEnvironment } from "../environment";

// Pieces the Logs page's views share: the search field, service names.

export const route = getRouteApi("/_auth/p/$projectId");

export function SearchField({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
}) {
  return (
    <label className="flex h-7 w-64 items-center gap-2 rounded-md border border-line px-2 focus-within:border-primary">
      <Search size={12} strokeWidth={1.6} className="text-faint" aria-hidden />
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        aria-label={placeholder}
        className="min-w-0 flex-1 bg-transparent font-mono text-2xs text-ink outline-none placeholder:font-sans placeholder:text-faint"
      />
    </label>
  );
}

/** One muted hue per service so interleaved lines read apart. Never the accent. */
const SERVICE_TONES = [
  { tone: "text-[#5c5f9a]", color: "#5c5f9a" },
  { tone: "text-[#4f7a5c]", color: "#4f7a5c" },
  { tone: "text-[#8a5f3c]", color: "#8a5f3c" },
  { tone: "text-[#8a4f6e]", color: "#8a4f6e" },
  { tone: "text-[#3c7a8a]", color: "#3c7a8a" },
  { tone: "text-[#8a7a3c]", color: "#8a7a3c" },
] as const;

const UNKNOWN_TONE = { tone: "text-faint", color: "var(--color-faint)" };

export type ServiceLabel = { text: string; tone: string; color: string };

/**
 * Names and tones of the environment's services. Log lines name a service by node id; spans by
 * their OTel `service.name`, which matches a node when the app sets it to the node's name, and
 * then wears that node's tone.
 */
export function useServices() {
  const { environmentId } = useEnvironment();
  const { data: nodes } = useListNodes(
    { path: { id: environmentId } },
    { query: { select: (l) => l.nodes } },
  );
  return useMemo(() => {
    const byId = new Map<string, ServiceLabel>();
    const byName = new Map<string, ServiceLabel>();
    (nodes ?? []).forEach((n, i) => {
      const label = { text: n.name, ...(SERVICE_TONES[i % SERVICE_TONES.length] ?? UNKNOWN_TONE) };
      byId.set(n.id, label);
      byName.set(n.name, label);
    });
    return {
      ofLine: (id: string): ServiceLabel =>
        byId.get(id) ?? { text: id.slice(0, 8), ...UNKNOWN_TONE },
      ofSpan: (name: string): ServiceLabel =>
        byName.get(name) ?? { text: name, ...UNKNOWN_TONE },
    };
  }, [nodes]);
}
