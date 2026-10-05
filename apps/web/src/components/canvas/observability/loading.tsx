import { cn } from "@my-better-t-app/ui/lib/utils";

/**
 * The Observability page while its first stream loads: a moth circling a light is the spinner,
 * the line under it says what is loading. The moth is the one the Harvard Mark II's operators
 * pulled out of Relay #70 on 9 September 1947 and taped into the log book, next to "First actual
 * case of bug being found"; the caption quotes it. Held back a moment, so a fast load goes
 * straight to the data without a flash.
 */
export function StreamLoading({ what, className }: { what: string; className?: string }) {
  return (
    <div
      className={cn(
        "flex min-h-0 flex-1 flex-col items-center justify-center gap-5 px-6 pb-16 animate-in fade-in-0 duration-500 [animation-delay:200ms] [animation-fill-mode:both] motion-reduce:animate-none",
        className,
      )}
    >
      <div className="relative size-[88px]" aria-hidden>
        <span className="absolute inset-0 m-auto size-1.5 rounded-full bg-primary shadow-[0_0_14px_5px_rgba(31,75,255,0.22)]" />
        <div className="keel-orbit absolute inset-0">
          <Moth className="absolute top-0 left-1/2 -translate-x-1/2 rotate-90" />
        </div>
      </div>
      <p className="text-sm text-ink" role="status">
        Reading {what}…
      </p>
      <p className="font-mono text-2xs text-faint" aria-hidden>
        “First actual case of bug being found.” · Harvard Mark II log book, 9/9/1947
      </p>
    </div>
  );
}

/** A small moth seen from above, wings beating. Head up; the orbit turns it along its path. */
function Moth({ className }: { className?: string }) {
  return (
    <svg width="30" height="21" viewBox="0 0 48 34" className={className} aria-hidden>
      <g className="keel-flap" stroke="#9b8f7c" strokeWidth="0.6" strokeLinejoin="round">
        <path d="M23 12C17 7 8 4.5 3 6.5c.5 4.5 3.5 9 8 10.5 4 1 8.5.6 12-.5z" fill="#cfc5b4" />
        <path d="M25 12c6-5 15-7.5 20-5.5-.5 4.5-3.5 9-8 10.5-4 1-8.5.6-12-.5z" fill="#cfc5b4" />
        <path d="M23 16.5c-4.5 1-10 3.5-10.5 8 3 2.5 7.5.5 10.5-4z" fill="#c2b7a4" />
        <path d="M25 16.5c4.5 1 10 3.5 10.5 8-3 2.5-7.5.5-10.5-4z" fill="#c2b7a4" />
        <circle cx="12" cy="11.2" r="1.3" fill="#9b8f7c" stroke="none" />
        <circle cx="36" cy="11.2" r="1.3" fill="#9b8f7c" stroke="none" />
      </g>
      <ellipse cx="24" cy="17" rx="1.9" ry="8" fill="#8a7f6d" />
      <circle cx="24" cy="9" r="1.7" fill="#8a7f6d" />
      <path
        d="M23.4 7.8C22.4 6 20.9 4.8 19.2 4.3M24.6 7.8c1-1.8 2.5-3 4.2-3.5"
        fill="none"
        stroke="#8a7f6d"
        strokeWidth="0.9"
        strokeLinecap="round"
      />
    </svg>
  );
}
