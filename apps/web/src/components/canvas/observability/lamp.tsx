import type { Id } from "@my-better-t-app/backend/convex/_generated/dataModel";
import { cn } from "@my-better-t-app/ui/lib/utils";

import { CopyPrompt } from "../copy-prompt";

/**
 * The Observability page with nothing to show, drawn as a hanging lamp and a moth. Empty range:
 * the lamp is lit, now and then flickering like a bad bulb, and the moth flutters around it,
 * always facing the light; nothing else came. Failed: the lamp is out, the moth rests on the
 * shade, and the error says why. The moth is the one the Harvard Mark II's operators pulled out
 * of Relay #70 on 9 September 1947 and taped into the log book, next to "First actual case of bug
 * being found"; hovering the lit lamp quotes it. Motion lives in canvas.css (`keel-*`).
 */

/** The lamp's viewBox is 80 × 70, drawn at SCALE; the bulb is at (40, 54) in it. */
const SCALE = 1.7;
const LAMP = { w: 80 * SCALE, h: 70 * SCALE };
const BOX_W = 300;
const BULB_X = BOX_W / 2;
const BULB_Y = 54 * SCALE;
/** In a viewBox point of the lamp's, page px inside the box. */
const onLamp = (x: number, y: number) => ({ x: BULB_X + (x - 40) * SCALE, y: y * SCALE });
/** Where the resting moth sits: on the shade's right slope, head up the slope, a little out. */
const PERCH = onLamp(55, 41);

export function StreamEmpty({
  long,
  environmentId,
  stale,
  className,
}: {
  /** The range, e.g. "hour". */
  long: string;
  environmentId: Id<"environments">;
  /** A new range is loading: dimmed, as the stream would be. */
  stale: boolean;
  className?: string;
}) {
  return (
    <Stage
      className={cn(
        "animate-in fade-in-0 duration-300 transition-opacity",
        stale && "opacity-60",
        className,
      )}
    >
      <div
        className="relative"
        style={{ width: BOX_W, height: 220 }}
        title="“First actual case of bug being found.” Harvard Mark II log book, 9 September 1947"
        aria-hidden
      >
        <div
          className="keel-flicker absolute rounded-full"
          style={{
            width: 340,
            height: 340,
            left: BULB_X - 170,
            top: BULB_Y - 170,
            background:
              "radial-gradient(circle, rgba(255, 207, 102, 0.34) 0%, rgba(255, 207, 102, 0.12) 32%, transparent 68%)",
          }}
        />
        <Lamp on />
        {/* Three layers, same start: across, up and down, and turning to face the light. */}
        <div className="absolute" style={{ left: BULB_X, top: BULB_Y + 62 }}>
          <div className="keel-moth-x">
            <div className="keel-moth-y">
              <div className="keel-moth-face -translate-x-1/2 -translate-y-1/2">
                <Moth flapping />
              </div>
            </div>
          </div>
        </div>
      </div>
      <p className="text-base text-ink">Nothing in the last {long} but a moth.</p>
      <CopyPrompt environmentId={environmentId} />
    </Stage>
  );
}

export function StreamError({
  what,
  error,
  className,
}: {
  what: string;
  error: string;
  className?: string;
}) {
  return (
    <Stage className={className}>
      <div className="relative" style={{ width: BOX_W, height: LAMP.h + 12 }} aria-hidden>
        <Lamp on={false} />
        <div
          className="absolute -translate-x-1/2 -translate-y-1/2 -rotate-[37deg]"
          style={{ left: PERCH.x, top: PERCH.y }}
        >
          <Moth />
        </div>
      </div>
      <div className="flex max-w-[520px] flex-col items-center gap-2 text-center" role="alert">
        <p className="text-base text-ink">Couldn’t read {what}</p>
        <p className="font-mono text-xs break-words text-danger">{error}</p>
        <p className="text-xs text-muted-foreground">Trying again every 10 seconds.</p>
      </div>
    </Stage>
  );
}

function Stage({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <div
      className={cn(
        "flex min-h-0 flex-1 flex-col items-center justify-center gap-4 px-6 pb-16 [animation-fill-mode:both] motion-reduce:animate-none",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** A pendant lamp: cord, dark shade, the bulb under it. Off: grey bulb, no light under the shade. */
function Lamp({ on }: { on: boolean }) {
  return (
    <svg
      width={LAMP.w}
      height={LAMP.h}
      viewBox="0 0 80 70"
      className="absolute top-0 left-1/2 -translate-x-1/2"
    >
      <line x1="40" y1="0" x2="40" y2="30" stroke="#c5cad3" strokeWidth="1" />
      <rect x="36.5" y="28" width="7" height="5.5" rx="1.2" fill="#1f2633" />
      <path d="M34 33h12l14 18.5c-6 2-13 3-20 3s-14-1-20-3z" fill="#1f2633" />
      <g className={on ? "keel-flicker" : undefined}>
        <ellipse cx="40" cy="51.6" rx="19" ry="2.4" fill={on ? "#fbe3a0" : "#2b3240"} />
        <circle cx="40" cy="54" r="5.5" fill={on ? "#fff4cf" : "#dfe2e7"} />
      </g>
    </svg>
  );
}

/** A small moth seen from above, head up. */
function Moth({ flapping = false }: { flapping?: boolean }) {
  return (
    <svg width="48" height="34" viewBox="0 0 48 34" className="block">
      <g
        className={flapping ? "keel-flap" : undefined}
        stroke="#9b8f7c"
        strokeWidth="0.6"
        strokeLinejoin="round"
      >
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
