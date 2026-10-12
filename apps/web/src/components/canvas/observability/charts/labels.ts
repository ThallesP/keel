const pad = (n: number) => String(n).padStart(2, "0");
const clock = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}`;
const day = (d: Date) => d.toLocaleDateString(undefined, { month: "short", day: "numeric" });

export function tickLabel(ms: number, bucketMs: number) {
  const d = new Date(ms);
  return bucketMs >= 60 * 60_000 ? `${day(d)} ${clock(d)}` : clock(d);
}

export function rangeLabel(ms: number, bucketMs: number) {
  const from = new Date(ms);
  const to = new Date(ms + bucketMs);
  const head = bucketMs >= 60 * 60_000 ? `${day(from)} ` : "";
  const secs = bucketMs < 60_000;
  const t = (d: Date) => (secs ? `${clock(d)}:${pad(d.getSeconds())}` : clock(d));
  return `${head}${t(from)}–${t(to)}`;
}
