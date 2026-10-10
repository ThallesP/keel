import { getRouteApi } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useMemo } from "react";

import { useListNodes } from "@/api/gen";

import { useEnvironment } from "../environment";

// Pieces the Observability page's views share: the search field, service names.

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
  "text-[#5c5f9a]",
  "text-[#4f7a5c]",
  "text-[#8a5f3c]",
  "text-[#8a4f6e]",
  "text-[#3c7a8a]",
  "text-[#8a7a3c]",
] as const;

export type ServiceLabel = { text: string; tone: string };

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
      const label = { text: n.name, tone: SERVICE_TONES[i % SERVICE_TONES.length] ?? "text-faint" };
      byId.set(n.id, label);
      byName.set(n.name, label);
    });
    return {
      ofLine: (id: string): ServiceLabel =>
        byId.get(id) ?? { text: id.slice(0, 8), tone: "text-faint" },
      ofSpan: (name: string): ServiceLabel =>
        byName.get(name) ?? { text: name, tone: "text-faint" },
    };
  }, [nodes]);
}
