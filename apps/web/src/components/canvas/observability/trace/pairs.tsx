import type { Attribute } from "@/gen/api";

export function Pairs({ pairs }: { pairs: Attribute[] }) {
  return (
    <dl className="grid grid-cols-[fit-content(55%)_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-2xs">
      {pairs.map(({ key, value }, i) => (
        <div key={`${key}:${i}`} className="contents">
          <dt className="wrap-anywhere text-faint">{key}</dt>
          <dd className="wrap-anywhere whitespace-pre-wrap text-ink">{value}</dd>
        </div>
      ))}
    </dl>
  );
}
