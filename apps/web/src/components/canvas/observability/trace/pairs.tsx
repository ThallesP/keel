import type { Attribute } from "@/api/gen";

export function Pairs({ pairs }: { pairs: Attribute[] }) {
  return (
    <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-3 gap-y-1 font-mono text-2xs">
      {pairs.map(({ key, value }, i) => (
        <div key={`${key}:${i}`} className="contents">
          <dt className="break-all text-faint">{key}</dt>
          <dd className="break-all whitespace-pre-wrap text-ink">{value}</dd>
        </div>
      ))}
    </dl>
  );
}
