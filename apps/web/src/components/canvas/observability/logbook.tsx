import { cn } from "@my-better-t-app/ui/lib/utils";

import { useNow } from "../use-now";

/**
 * The Observability page while its first stream loads: the Harvard Mark II log book's page of
 * 9 September 1947, where the operators taped the moth they pulled out of a relay next to "First
 * actual case of bug being found". Its entries come in one at a time, like a tail; under them
 * sits today's entry, which says what is loading. Entries as transcribed from the page; the
 * morning ones are left out because their handwriting reads several ways.
 */

const ENTRIES: { at: string; lines: string[] }[] = [
  { at: "1100", lines: ["Started Cosine Tape (Sine check)"] },
  { at: "1525", lines: ["Started Mult + Adder Test."] },
  {
    at: "1545",
    lines: ["Relay #70 Panel F", "(moth) in relay.", "First actual case of bug being found."],
  },
  { at: "1630", lines: ["Arctangent started."] },
  { at: "1700", lines: ["Closed down."] },
];

/** Each entry's first line's place in the tail: lines come in one by one. */
const TIMED = ENTRIES.reduce<{ at: string; lines: string[]; first: number }[]>(
  (out, e) => [...out, { ...e, first: out.reduce((n, o) => n + o.lines.length, 0) }],
  [],
);

const HOLD_MS = 200;
const STEP_MS = 260;
const appear = (row: number) => ({
  animationDelay: `${HOLD_MS + row * STEP_MS}ms`,
  animationFillMode: "both",
});
const entering =
  "animate-in fade-in-0 slide-in-from-bottom-1 duration-300 motion-reduce:animate-none";

/** `what`: what is loading, e.g. "the last hour of requests and logs". */
export function Logbook({ what, className }: { what: string; className?: string }) {
  const now = useNow(30_000);
  const hhmm = new Date(now).toTimeString().slice(0, 5).replace(":", "");

  return (
    // Held back a moment, so a fast load goes straight to the data without a flash of text.
    <div
      className={cn(
        "flex min-h-0 flex-1 items-center justify-center px-6 py-10 animate-in fade-in-0 duration-300 motion-reduce:animate-none",
        className,
      )}
      style={{ animationDelay: `${HOLD_MS}ms`, animationFillMode: "both" }}
    >
      <div className="flex w-full max-w-[460px] flex-col gap-6 font-mono text-xs">
        <span className="flex items-center gap-2 text-2xs text-muted-foreground" aria-hidden>
          <span className="size-1.5 bg-primary" />
          Log book, 9/9 (*)
        </span>
        <div className="flex flex-col gap-1.5" aria-hidden>
          {TIMED.map((e) => (
            <div key={e.at} className="relative flex gap-5">
              <span className={cn("w-9 shrink-0 text-faint", entering)} style={appear(e.first)}>
                {e.at}
              </span>
              <span className="flex flex-col gap-1.5 text-ink">
                {e.lines.map((line, i) => (
                  <span key={line} className={entering} style={appear(e.first + i)}>
                    {line}
                  </span>
                ))}
              </span>
              {/* On the page the moth is taped right of the entry, with "(moth) in relay." */}
              {e.at === "1545" && (
                <Moth
                  className="absolute top-[-2px] left-[192px] animate-in fade-in-0 zoom-in-90 duration-500 motion-reduce:animate-none"
                  style={appear(e.first + 1)}
                />
              )}
            </div>
          ))}
        </div>
        <div className="flex gap-5 border-t border-dashed border-line pt-4" role="status">
          <span className="w-9 shrink-0 text-primary" aria-hidden>
            {hhmm}
          </span>
          <span className="text-muted-foreground">
            Reading {what}
            <span
              className="ml-1.5 inline-block h-3 w-1.5 animate-pulse bg-primary align-middle"
              aria-hidden
            />
          </span>
        </div>
        <span className="text-2xs leading-relaxed text-faint" aria-hidden>
          (*) Harvard Mark II log book, 9 September 1947. The moth is still taped to the page, at
          the Smithsonian.
        </span>
      </div>
    </div>
  );
}

/** A faded moth under a strip of tape, the way it sits on the page. */
function Moth({ className, style }: { className?: string; style?: React.CSSProperties }) {
  return (
    <svg
      width="58"
      height="41"
      viewBox="0 0 48 34"
      className={cn("pointer-events-none -rotate-12", className)}
      style={style}
      aria-hidden
    >
      <g stroke="#9b8f7c" strokeWidth="0.5" strokeLinejoin="round">
        <path d="M23 12C17 7 8 4.5 3 6.5c.5 4.5 3.5 9 8 10.5 4 1 8.5.6 12-.5z" fill="#cfc5b4" />
        <path d="M25 12c6-5 15-7.5 20-5.5-.5 4.5-3.5 9-8 10.5-4 1-8.5.6-12-.5z" fill="#cfc5b4" />
        <path d="M23 16.5c-4.5 1-10 3.5-10.5 8 3 2.5 7.5.5 10.5-4z" fill="#c2b7a4" />
        <path d="M25 16.5c4.5 1 10 3.5 10.5 8-3 2.5-7.5.5-10.5-4z" fill="#c2b7a4" />
      </g>
      <g fill="none" stroke="#a89c88" strokeWidth="0.45" strokeLinecap="round">
        <path d="M21.5 13C16 10.5 10 9 5.5 8.5M21.5 14.5c-4 0-8.5.5-12 1.5" />
        <path d="M26.5 13c5.5-2.5 11.5-4 16-4.5M26.5 14.5c4 0 8.5.5 12 1.5" />
      </g>
      <circle cx="12" cy="11.2" r="1.1" fill="#9b8f7c" />
      <circle cx="36" cy="11.2" r="1.1" fill="#9b8f7c" />
      <ellipse cx="24" cy="17" rx="1.8" ry="8" fill="#8a7f6d" />
      <circle cx="24" cy="9" r="1.6" fill="#8a7f6d" />
      <path
        d="M23.4 7.8C22.4 6 20.9 4.8 19.2 4.3M24.6 7.8c1-1.8 2.5-3 4.2-3.5"
        fill="none"
        stroke="#8a7f6d"
        strokeWidth="0.8"
        strokeLinecap="round"
      />
      <rect
        x="5"
        y="12.5"
        width="38"
        height="8.5"
        transform="rotate(-5 24 16.75)"
        fill="rgba(236, 228, 205, 0.6)"
        stroke="rgba(11, 18, 32, 0.05)"
        strokeWidth="0.4"
      />
    </svg>
  );
}
